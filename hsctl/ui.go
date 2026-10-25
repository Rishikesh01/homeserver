package main

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// markdown renders with GFM enabled (tables, autolinks, strikethrough).
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

const (
	sessionCookie = "hsctl_session"
	sessionTTL    = 7 * 24 * time.Hour
)

type uiServer struct {
	repo string
	pass string

	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry

	// updSt caches the last update-check results for the Updates page (nil = not
	// checked since the dashboard started; cleared again after an apply).
	updMu sync.Mutex
	updSt []imageStatus
	updAt time.Time

	// Background jobs started from the dashboard (jobs.go), created on first use.
	jobsOnce sync.Once
	jobRun   *jobRunner

	// setupJob is the wizard's "Start everything" job, so its page can resume after a reconnect.
	setupMu  sync.Mutex
	setupJob string
}

// config returns the live, normalized config — the pair every handler needs together
// (Normalize fills derived fields, so a bare LoadConfig is never what you want).
func (s *uiServer) config() Config {
	c := LoadConfig(s.repo)
	c.Normalize()
	return c
}

func runUI(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	pass, err := uiPassword(repoDir())
	if err != nil {
		return err
	}
	s := &uiServer{repo: repoDir(), pass: pass, sessions: map[string]time.Time{}}
	c := s.config()

	// Reap expired sessions periodically so the token map can't grow without bound.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			s.sweepSessions()
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/help", s.handleHelp)
	mux.HandleFunc("/root.crt", s.handleCert)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/setup", s.requireAuth(s.handleSetup))
	mux.HandleFunc("/setup/up", s.requireAuth(s.handleSetupUp))
	mux.HandleFunc("/setup/up/status", s.requireAuth(s.handleSetupUpStatus))
	mux.HandleFunc("/admin", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/admin/action", s.requireAuth(s.handleAction))
	mux.HandleFunc("/admin/commands", s.requireAuth(s.handleCommands))
	mux.HandleFunc("/admin/run", s.requireAuth(s.handleRun))
	mux.HandleFunc("/admin/job", s.requireAuth(s.handleJob))
	mux.HandleFunc("/admin/apps", s.requireAuth(s.handleApps))
	mux.HandleFunc("/admin/apps/toggle", s.requireAuth(s.handleAppsToggle))
	mux.HandleFunc("/admin/updates", s.requireAuth(s.handleUpdatesPage))
	mux.HandleFunc("/admin/updates/check", s.requireAuth(s.handleUpdatesCheck))
	mux.HandleFunc("/admin/updates/apply", s.requireAuth(s.handleUpdatesApply))
	mux.HandleFunc("/admin/devices", s.requireAuth(s.handleDevices))
	mux.HandleFunc("/admin/devices/mount", s.requireAuth(s.handleDeviceMount))
	mux.HandleFunc("/admin/devices/unmount", s.requireAuth(s.deviceActionHandler(
		unmountDevice, "Eject failed: ", func(mp string) string { return "Ejected (unmounted " + mp + ") — safe to unplug." })))
	mux.HandleFunc("/admin/terminal", s.requireAuth(s.handleTerminalPage))
	mux.HandleFunc("/admin/terminal/ws", s.requireAuth(s.handleTerminalWS))
	mux.HandleFunc("/admin/assets/", s.requireAuth(s.handleAsset))
	mux.HandleFunc("/admin/backup", s.requireAuth(s.handleBackup))
	mux.HandleFunc("/admin/backup/config", s.requireAuth(s.handleBackupConfig))
	mux.HandleFunc("/admin/backup/restore", s.requireAuth(s.handleBackupRestore))
	mux.HandleFunc("/admin/backup/restore/progress", s.requireAuth(s.handleBackupRestoreProgress))
	fmt.Printf("hsctl ui:\n")
	fmt.Printf("  dashboard : https://%s/   (via Caddy — the LAN entrypoint)\n", c.ServerIP)
	fmt.Printf("  admin     : https://%s/admin\n", c.ServerIP)
	fmt.Printf("  login     : user 'admin', password in %s\n", filepath.Join(s.repo, ".ui-password"))

	handler := logRequests(mux)
	// An explicit --addr is honoured verbatim (dev runs, and the sandbox which forwards a port).
	if addr != "" {
		fmt.Printf("  listening : %s   (explicit --addr)\n", addr)
		return http.ListenAndServe(addr, handler)
	}
	// Default: the dashboard is plain HTTP and grants a root shell, so keep it OFF the LAN.
	// Bind loopback (local use) + the docker bridge gateway (so Caddy, which fronts us with
	// HTTPS via host.docker.internal, can still reach us) — never the LAN interface.
	addrs, warn := uiListenAddrs(dashboardPort(s.repo, c))
	if warn != "" {
		fmt.Fprintln(os.Stderr, warn)
	}
	return serveUI(handler, addrs)
}

// dashboardPort is the port the dashboard must listen on: the one Caddy forwards to (caddy/.env's
// HOME_UPSTREAM), so a UI_PORT edited in setup.conf but not yet applied with `hsctl setup` can't
// leave Caddy proxying to a port nothing listens on. Falls back to the configured port.
func dashboardPort(repo string, c Config) int {
	if kv, err := readKV(filepath.Join(repo, "caddy/.env")); err == nil {
		if up := kv["HOME_UPSTREAM"]; strings.HasPrefix(up, "host.docker.internal:") {
			if p := portFromUpstream(up); p > 0 {
				return p
			}
		}
	}
	return c.UIPort
}

// uiListenAddrs returns the addresses the dashboard binds to by default: always loopback, plus
// the docker bridge gateway so Caddy can reach it over the host gateway — without exposing the
// plaintext dashboard on the LAN. If the bridge gateway can't be detected it falls back to all
// interfaces (so the dashboard stays reachable) and returns a warning to print.
func uiListenAddrs(port int) (addrs []string, warn string) {
	addrs = []string{fmt.Sprintf("127.0.0.1:%d", port)}
	if gw := dockerBridgeGateway(); gw != "" {
		return append(addrs, fmt.Sprintf("%s:%d", gw, port)), ""
	}
	return []string{fmt.Sprintf("0.0.0.0:%d", port)},
		fmt.Sprintf("warning: could not detect the docker bridge gateway, so the dashboard is bound\n"+
			"on ALL interfaces (reachable as plain http on the LAN). Keep its port off your router,\n"+
			"or restrict it with:  hsctl ui --addr 127.0.0.1:%d", port)
}

// dockerBridgeGateway returns the IPv4 gateway of docker's default bridge (e.g. 172.17.0.1) —
// the address host.docker.internal resolves to, i.e. where Caddy connects to reach the host.
// The bridge can have several IPAM configs (IPv4 + IPv6), so we pick the first IPv4 one rather
// than concatenating them into a malformed address.
func dockerBridgeGateway() string {
	out, err := dockerOut(repoDir(), "network", "inspect", "bridge", "-f",
		"{{range .IPAM.Config}}{{.Gateway}} {{end}}")
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(out) {
		if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
			return f
		}
	}
	return ""
}

// serveUI serves mux on every address, returning when the first listener stops. Binding is
// all-or-nothing: a partial bind is worse than none — coming up on loopback but not the docker
// bridge gateway would look healthy to systemd while Caddy (its only route to us) gets
// connection-refused, so we fail and let the unit restart and retry cleanly.
func serveUI(mux http.Handler, addrs []string) error {
	srv := &http.Server{Handler: mux}
	var lns []net.Listener
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return fmt.Errorf("cannot listen on %s: %w", a, err)
		}
		fmt.Printf("  listening : http://%s/\n", a)
		lns = append(lns, ln)
	}
	errc := make(chan error, len(lns))
	for _, ln := range lns {
		go func(l net.Listener) { errc <- srv.Serve(l) }(ln)
	}
	return <-errc
}

// uiPassword returns the admin password from $HSCTL_UI_PASSWORD or .ui-password,
// generating and saving one on first run.
func uiPassword(repo string) (string, error) {
	if p := os.Getenv("HSCTL_UI_PASSWORD"); p != "" {
		return p, nil
	}
	path := filepath.Join(repo, ".ui-password")
	if b, err := os.ReadFile(path); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p, nil
		}
	}
	// Never mint a new password over one we merely can't read: the running service would keep
	// the old one and the file would no longer say what it is.
	if unreadable(path) {
		return "", errUnreadable(repo, []string{".ui-password"})
	}
	p := genPassword(16)
	if err := writeFile0600(path, p+"\n"); err != nil {
		return "", fmt.Errorf("saving the dashboard password: %w", err)
	}
	return p, nil
}

// requireAuth gates admin handlers behind a login-form session cookie. We use a cookie
// (not HTTP Basic Auth) because browser password managers like Bitwarden/Vaultwarden can't
// autofill the native Basic-Auth dialog — but they happily fill a normal login form.
func (s *uiServer) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// sweepSessions deletes every expired token. validSession only drops the one token it's handed,
// so without a periodic sweep the map grows unbounded with sessions from devices that logged in
// once and never came back. Called on a timer from runUI.
func (s *uiServer) sweepSessions() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, tok)
		}
	}
}

// validSession reports whether the request carries a live (unexpired) session cookie.
func (s *uiServer) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

type loginData struct{ Err, Next string }

// handleLogin shows the sign-in form (GET) and checks credentials (POST). On success it
// mints a session token, stores it server-side, and sets it as an HttpOnly cookie.
func (s *uiServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		u := r.FormValue("username")
		p := r.FormValue("password")
		ok := subtle.ConstantTimeCompare([]byte(u), []byte("admin")) == 1 &&
			subtle.ConstantTimeCompare([]byte(p), []byte(s.pass)) == 1
		if !ok {
			uiLog.Warn("login failed", "user", u, "from", remoteIP(r))
			w.WriteHeader(http.StatusUnauthorized)
			render(w, loginTmpl, loginData{Err: "Wrong username or password.", Next: safeNext(r.FormValue("next"))})
			return
		}
		uiLog.Info("login ok", "from", remoteIP(r))
		tok := genPassword(32)
		s.mu.Lock()
		s.sessions[tok] = time.Now().Add(sessionTTL)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
			MaxAge: int(sessionTTL / time.Second),
		})
		http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
		return
	}
	render(w, loginTmpl, loginData{Next: safeNext(r.URL.Query().Get("next"))})
}

func (s *uiServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps post-login redirects on this site (a local path), defaulting to /admin. It
// must reject anything that resolves off-site: a protocol-relative "//host", and — because
// browsers fold "\" into "/" per the URL spec — any backslash (so "/\host" can't sneak through
// as "//host"). A legitimate in-app path never contains a backslash.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.ContainsRune(next, '\\') {
		return next
	}
	return "/admin"
}

// isHTTPS reports whether the browser reached us over TLS, so we only flag the cookie
// Secure then. Caddy terminates TLS and proxies in plain HTTP, so trust its forwarded
// header; direct http://host:port access (no TLS) keeps the cookie usable.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

type containerStatus struct {
	Name    string
	State   string
	Status  string
	Running bool
}

func (s *uiServer) status() ([]containerStatus, error) {
	args := []string{"ps", "-a", "--format", "{{.Names}}|{{.State}}|{{.Status}}"}
	for _, n := range stackContainers {
		args = append(args, "--filter", "name="+n)
	}
	out, err := dockerOut(s.repo, args...)
	if err != nil {
		return nil, err
	}
	var res []containerStatus
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		p := strings.SplitN(line, "|", 3)
		if len(p) < 3 {
			continue
		}
		res = append(res, containerStatus{p[0], p[1], p[2], p[1] == "running"})
	}
	return res, nil
}

type serviceLink struct{ Name, Icon, Desc, URL string }

type homeData struct {
	Cfg      Config
	Services []serviceLink
}

func (s *uiServer) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// Fresh install (hsctl install ran, nothing configured yet): the home page has nothing to
	// show, so take the admin straight into the wizard.
	if !s.setupDone() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	c := s.config()
	var links []serviceLink
	for _, svc := range LoadServices(s.repo) {
		if svc.Dir != "" && c.IsDisabled(svc.Dir) {
			continue
		}
		links = append(links, serviceLink{svc.Name, svc.Icon, svc.Desc, svc.URL(c.ServerIP)})
	}
	render(w, homeTmpl, homeData{Cfg: c, Services: links})
}

type helpData struct{ Body template.HTML }

// handleHelp renders ONBOARDING.md (with SERVER_IP filled in) as an in-dashboard guide.
func (s *uiServer) handleHelp(w http.ResponseWriter, r *http.Request) {
	c := s.config()
	md, err := os.ReadFile(filepath.Join(s.repo, "ONBOARDING.md"))
	if err != nil {
		http.Error(w, "setup guide not available", http.StatusNotFound)
		return
	}
	src := strings.ReplaceAll(string(md), "SERVER_IP", c.ServerIP)
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, helpTmpl, helpData{Body: template.HTML(buf.String())})
}

type adminData struct {
	Cfg        Config
	Containers []containerStatus
	DockerErr  string
	Msg        string
	Sys        sysStats
	Backup     backupFreshness
	SetupDone  bool
	Busy       string // what's running right now, "" if nothing
	JobID      string // the dashboard job to follow, if one is running
}

func (s *uiServer) handleAdmin(w http.ResponseWriter, r *http.Request) {
	d := adminData{Cfg: s.config(), Msg: r.URL.Query().Get("msg"), Backup: backupFreshnessFor(s.repo), SetupDone: s.setupDone()}
	d.Busy, d.JobID = s.busyNote()
	// gatherSysStats blocks ~300ms for its CPU sample, so overlap it with docker ps.
	sysCh := make(chan sysStats, 1)
	go func() { sysCh <- gatherSysStats(s.repo) }()
	st, err := s.status()
	if err != nil {
		uiLog.Warn("docker unreachable", "err", err)
		d.DockerErr = "Docker is not reachable. Is the daemon running, and is this user in the 'docker' group? (sudo usermod -aG docker $USER, then re-login)"
	}
	d.Containers = st
	d.Sys = <-sysCh
	render(w, adminTmpl, d)
}

// handleAction powers the machine off. Start/Stop/Restart on the admin page are Command Center
// jobs (up / down --keep-proxy / restart), so they run to the end even if the page is closed.
func (s *uiServer) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("do") != "shutdown" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	// Powering off must not cut a backup or restore short.
	l, err := acquireOpLock(s.repo, "dashboard: shutdown", 0)
	if err != nil {
		http.Redirect(w, r, "/admin?msg="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	defer l.Release()
	msg := s.runShutdown()
	uiLog.Info("admin action", "do", "shutdown", "from", remoteIP(r))
	http.Redirect(w, r, "/admin?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}

// runShutdown powers the whole machine off. The poweroff is deferred to a goroutine so
// this HTTP response (the confirmation flash) can flush first — once it fires, the box
// goes down and the UI stops responding.
//
// We deliberately do NOT `compose down`/`stop` the stack first: systemd stops docker
// cleanly on the way down (containers get SIGTERM with a grace period), and because every
// service uses `restart: unless-stopped`, they all come back automatically on next boot.
// Explicitly stopping them here would instead mark them "stopped" and they would stay down
// after a power cycle — bad for a server the family just switches on and off.
func (s *uiServer) runShutdown() string {
	go func() {
		time.Sleep(2 * time.Second)
		uiLog.Warn("powering off the machine (dashboard shutdown button)")
		_ = privCmd("shutdown", "-h", "now").Run()
	}()
	return "Shutting down — the server is powering off now. This page will go offline. " +
		"When you switch the machine back on, the apps start again automatically."
}

// ---- Command Center ---------------------------------------------------------

type cmdGroup struct {
	Name string
	Cmds []webCmd
}

type commandsData struct {
	Groups      []cmdGroup
	Busy, JobID string
}

// handleCommands renders every CLI command as an explained, runnable card, grouped by
// category. Running starts a background job via /admin/run, so this page is read-only.
func (s *uiServer) handleCommands(w http.ResponseWriter, r *http.Request) {
	var groups []cmdGroup
	for _, cat := range webCmdCategories {
		g := cmdGroup{Name: cat}
		for _, c := range webCmds {
			if c.Category == cat {
				g.Cmds = append(g.Cmds, c)
			}
		}
		if len(g.Cmds) > 0 {
			groups = append(groups, g)
		}
	}
	d := commandsData{Groups: groups}
	d.Busy, d.JobID = s.busyNote()
	render(w, commandsTmpl, d)
}

// ---- Devices (list + guided mount) ------------------------------------------

type devicesData struct {
	Rows      []deviceRow
	Err       string
	Msg       string
	MountAt   string // where mounts land (/mnt), shown to the user
	GuardPath string // configured backup REQUIRE_MOUNT path, "" if none
	GuardOK   bool   // is that path a real mount right now
}

func (s *uiServer) handleDevices(w http.ResponseWriter, r *http.Request) {
	cfg := loadBackupCfg(s.repo)
	d := devicesData{Msg: r.URL.Query().Get("msg"), MountAt: mountRoot, GuardPath: cfg.RequireMount}
	if cfg.RequireMount != "" {
		d.GuardOK = requireBackupMount(cfg) == nil // is the backup disk already mounted there?
	}
	devs, err := listBlockDevices()
	if err != nil {
		d.Err = err.Error()
	} else {
		d.Rows = mountableRows(devs)
	}
	render(w, devicesTmpl, d)
}

// handleDeviceMount mounts the posted device at the directory the operator chose in the form
// (the same freedom as a manual `mount <disk> <dir>`). A blank target falls back to the
// per-label default under /mnt; mountDeviceAt validates both the device and the directory.
func (s *uiServer) handleDeviceMount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
		return
	}
	target := strings.TrimSpace(r.FormValue("target"))
	dev := strings.TrimSpace(r.FormValue("dev"))
	var msg string
	if mp, err := mountDeviceAt(dev, target); err != nil {
		uiLog.Warn("mount failed", "dev", dev, "target", target, "err", err)
		msg = "Mount failed: " + err.Error()
	} else {
		uiLog.Info("mounted device", "dev", dev, "at", mp, "from", remoteIP(r))
		msg = "Mounted at " + mp
	}
	http.Redirect(w, r, "/admin/devices?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}

// deviceActionHandler builds the POST handler for a device action (currently Eject): run it
// on the posted device, then flash the result back on /admin/devices.
func (s *uiServer) deviceActionHandler(action func(string) (string, error), errPrefix string, ok func(string) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
			return
		}
		var msg string
		dev := strings.TrimSpace(r.FormValue("dev"))
		if res, err := action(dev); err != nil {
			uiLog.Warn("device action failed", "dev", dev, "err", err)
			msg = errPrefix + err.Error()
		} else {
			uiLog.Info("device action ok", "dev", dev, "result", res, "from", remoteIP(r))
			msg = ok(res)
		}
		http.Redirect(w, r, "/admin/devices?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
	}
}

// ---- Terminal page (the WebSocket handler lives in terminal.go) --------------

func (s *uiServer) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	render(w, terminalTmpl, nil)
}

type backupData struct {
	Repo, Retention, Snapshots, Msg string
	Replica                         string // off-site replica repo, "" if unset
	ResticOK                        bool
	ResticVersion                   string
	GuardPath                       string // REQUIRE_MOUNT path, "" if unset
	GuardOK                         bool   // is that path a real mount right now
	Stats                           string // restic repo stats, "" if unavailable
	Busy, JobID                     string
	KeepDaily, KeepWeekly           string // the retention form's fields ("" = not set)
	KeepMonthly, KeepRest           string
	RetentionErr                    string // the saved policy is invalid (nothing gets pruned)
	Notify                          string // failure-notification URL, "" if unset
	Last                            backupFreshness
	Timer                           backupTimer
}

func (s *uiServer) handleBackup(w http.ResponseWriter, r *http.Request) {
	cfg := loadBackupCfg(s.repo)
	d := backupData{Repo: cfg.Repo, Retention: cfg.Retention, Replica: cfg.ReplicaRepo,
		ResticOK: resticInstalled(), Msg: r.URL.Query().Get("msg"), GuardPath: cfg.RequireMount,
		Notify: cfg.NotifyURL, Last: backupFreshnessFor(s.repo), Timer: backupTimerState()}
	if cfg.RequireMount != "" {
		d.GuardOK = requireBackupMount(cfg) == nil // is the backup disk actually mounted now?
	}
	if v, err := resticVersion(); err == nil {
		d.ResticVersion = v
	}
	d.Busy, d.JobID = s.busyNote()
	if _, err := parseRetention(cfg.Retention); err != nil {
		d.RetentionErr = err.Error()
	}
	daily, weekly, monthly, rest := retentionKeeps(cfg.Retention)
	field := func(n int) string {
		if n < 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	d.KeepDaily, d.KeepWeekly, d.KeepMonthly, d.KeepRest = field(daily), field(weekly), field(monthly), rest
	if d.ResticOK && requireResticPassword(s.repo) != nil {
		d.Snapshots = requireResticPassword(s.repo).Error()
	} else if d.ResticOK {
		if out, err := resticOutput(s.repo, cfg, "snapshots"); err != nil {
			d.Snapshots = "No snapshots yet — set a destination, then 'Initialize', then 'Back up now'.\n\n" + out
		} else {
			d.Snapshots = out
		}
		if out, err := resticOutput(s.repo, cfg, "stats", "--mode", "raw-data"); err == nil {
			d.Stats = strings.TrimSpace(out)
		}
	}
	render(w, backupTmpl, d)
}

func (s *uiServer) handleBackupConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/backup", http.StatusSeeOther)
		return
	}
	msg := "Destination saved"
	cfg := loadBackupCfg(s.repo)
	if v := strings.TrimSpace(r.FormValue("repo")); v != "" {
		cfg.Repo = v
	}
	_ = r.ParseForm()
	if r.Form.Has("keep_daily") {
		policy, err := buildRetention(r.FormValue("keep_daily"), r.FormValue("keep_weekly"),
			r.FormValue("keep_monthly"), r.FormValue("keep_rest"))
		if err != nil {
			http.Redirect(w, r, "/admin/backup?msg="+template.URLQueryEscaper("Not saved — "+err.Error()), http.StatusSeeOther)
			return
		}
		cfg.Retention = policy
	}
	if r.Form.Has("replica") {
		cfg.ReplicaRepo = strings.TrimSpace(r.FormValue("replica")) // an empty box clears it
	}
	if r.Form.Has("notify") {
		v := strings.TrimSpace(r.FormValue("notify"))
		if v != "" && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
			http.Redirect(w, r, "/admin/backup?msg="+template.URLQueryEscaper("Not saved — the notification address must be an http(s):// URL"), http.StatusSeeOther)
			return
		}
		cfg.NotifyURL = v
	}
	if err := cfg.save(s.repo); err != nil {
		msg = "Save failed: " + err.Error()
	}
	http.Redirect(w, r, "/admin/backup?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}

type restoreData struct {
	Snapshots, Msg string
	ResticOK       bool
}

// handleBackupRestore shows a confirmation page (GET) and starts the destructive DR put-back
// (POST) — but only when the operator types RESTORE. The put-back is `hsctl backup restore
// --into-volumes` run as a dashboard job: it extracts the snapshot, checks it, stops the stack,
// repopulates every volume (Vaultwarden from its staged copy) and brings the stack back up. The
// POST redirects to a progress page that follows the job, so a reload can't start a second
// restore, and the job runs to the end even while Caddy — this page's only route — restarts.
func (s *uiServer) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	cfg := loadBackupCfg(s.repo)
	if r.Method == http.MethodPost {
		if strings.TrimSpace(r.FormValue("confirm")) != "RESTORE" {
			http.Redirect(w, r, "/admin/backup/restore?msg="+
				template.URLQueryEscaper("Type RESTORE to confirm — nothing was changed."), http.StatusSeeOther)
			return
		}
		args := []string{"backup", "restore", "--into-volumes", "--yes"}
		snap := strings.TrimSpace(r.FormValue("snapshot"))
		if snap != "" {
			if strings.HasPrefix(snap, "-") || strings.ContainsAny(snap, " \t/") {
				http.Redirect(w, r, "/admin/backup/restore?msg="+
					template.URLQueryEscaper("That doesn't look like a snapshot ID — nothing was changed."), http.StatusSeeOther)
				return
			}
			args = append(args, snap)
		}
		uiLog.Warn("restore requested", "snapshot", snap, "from", remoteIP(r))
		if holder := opLockBusy(s.repo); holder != "" {
			http.Redirect(w, r, "/admin/backup/restore?msg="+template.URLQueryEscaper(
				"Another operation is running ("+holder+") — try again once it finishes."), http.StatusSeeOther)
			return
		}
		j, err := s.jobs().start(s.repo, "restore", "Restore from backup", args)
		if err != nil {
			http.Redirect(w, r, "/admin/backup/restore?msg="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/backup/restore/progress?id="+url.QueryEscape(j.ID), http.StatusSeeOther)
		return
	}
	d := restoreData{Msg: r.URL.Query().Get("msg"), ResticOK: resticInstalled()}
	if d.ResticOK {
		if out, err := resticOutput(s.repo, cfg, "snapshots"); err == nil {
			d.Snapshots = out
		}
	}
	render(w, restoreTmpl, d)
}

// handleBackupRestoreProgress is the page a restore redirects to: it follows the restore job's
// output, riding out the minutes the proxy is down mid-restore.
func (s *uiServer) handleBackupRestoreProgress(w http.ResponseWriter, r *http.Request) {
	render(w, restoreProgressTmpl, struct{ JobID string }{r.URL.Query().Get("id")})
}

// handleCert serves the public root CA (from the saved file, else extracted live).
func (s *uiServer) handleCert(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.repo, "caddy-root-ca.crt")
	if b, err := os.ReadFile(path); err == nil {
		serveCert(w, b)
		return
	}
	if out, err := dockerOut(s.repo, "exec", "caddy", "cat",
		"/data/caddy/pki/authorities/local/root.crt"); err == nil {
		serveCert(w, []byte(out+"\n"))
		return
	}
	http.Error(w, "certificate not available yet (is Caddy running?)", http.StatusServiceUnavailable)
}

func serveCert(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="homeserver-ca.crt"`)
	w.Write(b)
}

func reversed(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

var tmplFuncs = template.FuncMap{
	"css": func() template.CSS { return template.CSS(cssText) },
	// meterClass colours a usage bar by how full it is: green, amber from 70%, red from 90%.
	"meterClass": func(pct int) string {
		switch {
		case pct >= 90:
			return "hot"
		case pct >= 70:
			return "warn"
		default:
			return ""
		}
	},
}

func parseTmpl(tmpl string) (*template.Template, error) {
	return template.New("p").Funcs(tmplFuncs).Parse(tmpl)
}

func render(w http.ResponseWriter, tmpl string, data any) {
	t, err := parseTmpl(tmpl)
	if err != nil {
		uiLog.Error("template parse failed", "err", err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		uiLog.Error("template render failed", "err", err)
		http.Error(w, err.Error(), 500)
	}
}
