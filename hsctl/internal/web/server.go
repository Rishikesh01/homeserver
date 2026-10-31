package web

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/catalog"
	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/devices"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/jobs"
	"github.com/Rishikesh01/homeserver/hsctl/internal/oplock"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secret"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
	"github.com/Rishikesh01/homeserver/hsctl/internal/sysinfo"
	"github.com/Rishikesh01/homeserver/hsctl/internal/updates"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// markdown renders with GFM enabled (tables, autolinks, strikethrough).
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

const (
	sessionCookie = "hsctl_session"
	sessionTTL    = 7 * 24 * time.Hour
)

// Settings loads, normalizes and saves the stack's settings.
type Settings interface {
	Loaded() config.Settings
	Normalize(*config.Settings)
	Save(config.Settings) error
}

// Provisioner generates the configuration (the setup wizard).
type Provisioner interface {
	Generate(c config.Settings, force bool) (provision.Result, error)
	Configured() bool
}

// Stack reports on the stack's containers and apps, and hands out Caddy's certificate.
type Stack interface {
	Containers() ([]stack.Container, error)
	Apps(withState bool) []stack.App
	CA() ([]byte, error)
}

// Backup is the backup service as the Backups and restore pages use it.
type Backup interface {
	Settings() backup.Settings
	SaveSettings(backup.Settings) error
	SetPassword(cfg backup.Settings, pw string, w io.Writer) error
	RequirePassword() error
	RequireMount(backup.Settings) error
	DiskMissing(backup.Settings) string
	OnSystemDisk(path string) (bool, string)
	Freshness() backup.Freshness
	ReplicaAge() string
	Timer() backup.Timer
	Snapshots() (string, error)
	Stats() (string, error)
}

// Restic says whether restic is there, and which.
type Restic interface {
	Installed() bool
	Version() (string, error)
}

// Updates checks the running images against their registries.
type Updates interface {
	Check(progress func(updates.Status)) ([]updates.Status, error)
}

// Devices lists, mounts and ejects block devices.
type Devices interface {
	List() ([]devices.BlockDevice, error)
	Mount(dev, target string) (string, error)
	Unmount(dev string) (string, error)
}

// Sysinfo gathers the admin page's system section.
type Sysinfo interface {
	Gather() sysinfo.Stats
}

// Docker explains a Docker that can't be used, and finds the bridge gateway Caddy reaches us on.
type Docker interface {
	Problem() string
	BridgeGateway() string
}

// Jobs runs hsctl commands in the background for the pages to follow.
type Jobs interface {
	Start(slug, title string, args []string) (jobs.Job, error)
	Get(id string) (jobs.Job, bool)
	Running() *jobs.Job
}

// Locker is the shared operation lock.
type Locker interface {
	Acquire(what string, wait time.Duration) (*oplock.Lock, error)
	Busy() string
}

// Privileged runs a command as root (powering off).
type Privileged interface {
	Command(name string, args ...string) *exec.Cmd
}

// Deps are the services the dashboard drives.
type Deps struct {
	Settings  Settings
	Provision Provisioner
	Stack     Stack
	Backup    Backup
	Restic    Restic
	Updates   Updates
	Devices   Devices
	Sysinfo   Sysinfo
	Docker    Docker
	Jobs      Jobs
	Lock      Locker
	Priv      Privileged
}

// Server is the web dashboard: the household home page, and the admin pages behind a login.
type Server struct {
	Deps
	Repo paths.Repo
	Pass string       // the admin password
	Log  *slog.Logger // server-side events (the journal, under systemd)
	Out  io.Writer    // the startup banner

	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry

	// Login throttling (see loginBlocked): failures per client and per connection peer, plus a
	// lock so password checks run one at a time.
	loginMu      sync.Mutex
	loginFails   map[string]*loginFailures
	loginCheckMu sync.Mutex

	// updSt caches the last update-check results for the Updates page (nil = not checked since
	// the dashboard started; cleared again after an apply).
	updMu sync.Mutex
	updSt []updates.Status
	updAt time.Time

	// setupJob is the wizard's "Start everything" job, so its page can resume after a reconnect.
	setupMu  sync.Mutex
	setupJob string
}

// New returns the dashboard for repo, signing the admin in with pass.
func New(repo paths.Repo, pass string, d Deps) *Server {
	return &Server{Deps: d, Repo: repo, Pass: pass, sessions: map[string]time.Time{},
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)), Out: os.Stdout}
}

// Handler is the dashboard's routes, with request logging.
func (s *Server) Handler() http.Handler {
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
		s.Devices.Unmount, "Eject failed: ", func(mp string) string { return "Ejected (unmounted " + mp + ") — safe to unplug." })))
	mux.HandleFunc("/admin/terminal", s.requireAuth(s.handleTerminalPage))
	mux.HandleFunc("/admin/terminal/ws", s.requireAuth(s.handleTerminalWS))
	mux.HandleFunc("/admin/assets/", s.requireAuth(s.handleAsset))
	mux.HandleFunc("/admin/backup", s.requireAuth(s.handleBackup))
	mux.HandleFunc("/admin/backup/config", s.requireAuth(s.handleBackupConfig))
	mux.HandleFunc("/admin/backup/password", s.requireAuth(s.handleBackupPassword))
	mux.HandleFunc("/admin/backup/restore", s.requireAuth(s.handleBackupRestore))
	mux.HandleFunc("/admin/backup/restore/progress", s.requireAuth(s.handleBackupRestoreProgress))
	return s.logRequests(mux)
}

// Serve runs the dashboard until a listener fails. An explicit addr is honoured verbatim (dev
// runs, and the sandbox which forwards a port); otherwise it binds loopback and the docker bridge
// gateway — never the LAN, since the dashboard is plain HTTP and grants a root shell.
func (s *Server) Serve(addr string) error {
	c := s.Settings.Loaded()
	handler := s.Handler()
	// Reap expired sessions periodically so the token map can't grow without bound.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			s.sweepSessions()
		}
	}()
	fmt.Fprintf(s.Out, "hsctl ui:\n")
	fmt.Fprintf(s.Out, "  dashboard : https://%s/   (via Caddy — the LAN entrypoint)\n", c.ServerIP)
	fmt.Fprintf(s.Out, "  admin     : https://%s/admin\n", c.ServerIP)
	fmt.Fprintf(s.Out, "  login     : user 'admin', password in %s\n", s.Repo.Path(paths.UIPassword))
	if addr != "" {
		fmt.Fprintf(s.Out, "  listening : %s   (explicit --addr)\n", addr)
		return http.ListenAndServe(addr, handler)
	}
	// Bind loopback (local use) + the docker bridge gateway (so Caddy, which fronts us with HTTPS
	// via host.docker.internal, can still reach us) — never the LAN interface.
	addrs, warn := ListenAddrs(DashboardPort(s.Repo, c), s.Docker.BridgeGateway())
	if warn != "" {
		s.Log.Warn(warn)
	}
	return s.serve(handler, addrs)
}

// DashboardPort is the port the dashboard must listen on: the one Caddy forwards to (caddy/.env's
// HOME_UPSTREAM), so a UI_PORT edited in setup.conf but not yet applied with `hsctl setup` can't
// leave Caddy proxying to a port nothing listens on. Falls back to the configured port.
func DashboardPort(repo paths.Repo, c config.Settings) int {
	if kv, err := envfile.Read(repo.Path("caddy/.env")); err == nil {
		if up := kv["HOME_UPSTREAM"]; strings.HasPrefix(up, "host.docker.internal:") {
			if p := config.PortFromUpstream(up); p > 0 {
				return p
			}
		}
	}
	return c.UIPort
}

// ListenAddrs returns the addresses the dashboard binds to by default: always loopback, plus the
// docker bridge gateway so Caddy can reach it over the host gateway — without exposing the
// plaintext dashboard on the LAN. Without a gateway it falls back to all interfaces (so the
// dashboard stays reachable) and returns a warning to print.
func ListenAddrs(port int, gateway string) (addrs []string, warn string) {
	addrs = []string{fmt.Sprintf("127.0.0.1:%d", port)}
	if gateway != "" {
		return append(addrs, fmt.Sprintf("%s:%d", gateway, port)), ""
	}
	return []string{fmt.Sprintf("0.0.0.0:%d", port)},
		fmt.Sprintf("could not detect the docker bridge gateway, so the dashboard is bound on ALL interfaces "+
			"(reachable as plain http on the LAN). Keep its port off your router, or restrict it with: "+
			"hsctl ui --addr 127.0.0.1:%d", port)
}

// serve serves handler on every address, returning when the first listener stops. Binding is
// all-or-nothing: a partial bind is worse than none — coming up on loopback but not the docker
// bridge gateway would look healthy to systemd while Caddy (its only route to us) gets
// connection-refused, so we fail and let the unit restart and retry cleanly.
func (s *Server) serve(handler http.Handler, addrs []string) error {
	srv := &http.Server{Handler: handler}
	var lns []net.Listener
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return fmt.Errorf("cannot listen on %s: %w", a, err)
		}
		fmt.Fprintf(s.Out, "  listening : http://%s/\n", a)
		lns = append(lns, ln)
	}
	errc := make(chan error, len(lns))
	for _, ln := range lns {
		go func(l net.Listener) { errc <- srv.Serve(l) }(ln)
	}
	return <-errc
}

// requireAuth gates admin handlers behind a login-form session cookie. We use a cookie
// (not HTTP Basic Auth) because browser password managers like Bitwarden/Vaultwarden can't
// autofill the native Basic-Auth dialog — but they happily fill a normal login form.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !s.sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if !s.validSession(r) {
			// The pages' scripts poll these; a login redirect would look like a broken JSON reply,
			// so say plainly that the session is gone (e.g. the dashboard restarted).
			if r.URL.Path == "/admin/job" || r.URL.Path == "/admin/run" || r.URL.Path == "/setup/up/status" {
				http.Error(w, "signed out — sign in again", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// sameOrigin reports whether a state-changing request came from the dashboard's own pages. The
// session cookie is SameSite=Lax, but every app on this box shares the server's address on another
// port — and cookies ignore ports, so a page on any of them counts as "same site" and the browser
// would send our cookie along. Browsers always send Origin (or at least Referer) with a POST, so
// one from another origin is refused; a client sending neither (curl, scripts) isn't a browser
// carrying someone's cookie, and is let through.
func (s *Server) sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" || src == "null" {
		if ref := r.Header.Get("Referer"); ref != "" {
			src = ref
		} else if src == "null" {
			return false
		} else {
			return true
		}
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return false
	}
	got := u.Scheme + "://" + u.Host
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return got == scheme+"://"+r.Host || got == "https://"+s.Settings.Loaded().ServerIP
}

// sweepSessions deletes every expired token. validSession only drops the one token it's handed,
// so without a periodic sweep the map grows unbounded with sessions from devices that logged in
// once and never came back. Called on a timer from runUI.
func (s *Server) sweepSessions() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, tok)
		}
	}
	s.loginMu.Lock()
	for key, f := range s.loginFails {
		if now.Sub(f.last) > loginForgetAfter && now.After(f.until) {
			delete(s.loginFails, key)
		}
	}
	s.loginMu.Unlock()
}

// validSession reports whether the request carries a live (unexpired) session cookie.
func (s *Server) validSession(r *http.Request) bool {
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
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if !s.sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		ip := remoteIP(r)
		keys := loginKeys(r)
		if wait := s.loginBlocked(keys, time.Now()); wait > 0 {
			s.Log.Warn("login throttled", "from", ip)
			w.WriteHeader(http.StatusTooManyRequests)
			render(w, loginTmpl, loginData{Err: fmt.Sprintf("Too many wrong passwords — wait %s and try again.",
				jobs.ShortDuration(wait)), Next: safeNext(r.FormValue("next"))})
			return
		}
		u := r.FormValue("username")
		p := r.FormValue("password")
		// One password check at a time, with a pause after a wrong one, so parallel requests can't
		// slip a burst of guesses past the lockout. The lockout is checked again once it's our
		// turn: requests queued behind a failure must not get their guess compared anyway.
		s.loginCheckMu.Lock()
		if wait := s.loginBlocked(keys, time.Now()); wait > 0 {
			s.loginCheckMu.Unlock()
			s.Log.Warn("login throttled", "from", ip)
			w.WriteHeader(http.StatusTooManyRequests)
			render(w, loginTmpl, loginData{Err: fmt.Sprintf("Too many wrong passwords — wait %s and try again.",
				jobs.ShortDuration(wait)), Next: safeNext(r.FormValue("next"))})
			return
		}
		ok := subtle.ConstantTimeCompare([]byte(u), []byte("admin")) == 1 &&
			subtle.ConstantTimeCompare([]byte(p), []byte(s.Pass)) == 1
		if !ok {
			s.loginFailed(keys, time.Now())
			time.Sleep(400 * time.Millisecond)
		}
		s.loginCheckMu.Unlock()
		if !ok {
			s.Log.Warn("login failed", "user", u, "from", ip)
			w.WriteHeader(http.StatusUnauthorized)
			render(w, loginTmpl, loginData{Err: "Wrong username or password.", Next: safeNext(r.FormValue("next"))})
			return
		}
		s.loginSucceeded(keys)
		s.Log.Info("login ok", "from", ip)
		tok := secret.Password(32)
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

// Login throttling. The dashboard grants a root shell, so a password guesser gets a few free tries
// and then waits: after a few wrong passwords, each further failure locks them out for twice as
// long as the last (30s, 1m, 2m … up to 15m). A success resets it. Failures are counted per
// client (the address Caddy reports) AND per connection peer: the forwarded address can't be
// trusted from a direct connection (anything on the box can reach us without Caddy and claim any
// X-Forwarded-For), so the peer gets its own, larger allowance that rotating addresses can't
// escape. Every LAN client reaches us through Caddy — the same peer — so the peer's lockout is
// capped at a minute: enough to throttle guessing, too short for one noisy device to keep the
// admin out.
const (
	loginFreeTries     = 5  // per client
	loginPeerFreeTries = 30 // per connection peer (Caddy carries every LAN client)
	loginFirstLock     = 30 * time.Second
	loginMaxLock       = 15 * time.Minute
	loginPeerMaxLock   = time.Minute
	loginForgetAfter   = time.Hour // failures this old are forgotten
)

type loginFailures struct {
	count int
	last  time.Time
	until time.Time // locked out until then
}

// loginKeys are the throttle buckets a login request counts against: its client and its peer.
func loginKeys(r *http.Request) []string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	return []string{"client " + remoteIP(r), "peer " + peer}
}

func freeTries(key string) int {
	if strings.HasPrefix(key, "peer ") {
		return loginPeerFreeTries
	}
	return loginFreeTries
}

// loginBlocked returns how long the request must still wait, or 0.
func (s *Server) loginBlocked(keys []string, now time.Time) time.Duration {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	var wait time.Duration
	for _, k := range keys {
		if f := s.loginFails[k]; f != nil && now.Before(f.until) && f.until.Sub(now) > wait {
			wait = f.until.Sub(now)
		}
	}
	return wait
}

func (s *Server) loginFailed(keys []string, now time.Time) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.loginFails == nil {
		s.loginFails = map[string]*loginFailures{}
	}
	for _, k := range keys {
		f := s.loginFails[k]
		if f == nil || now.Sub(f.last) > loginForgetAfter {
			f = &loginFailures{}
			s.loginFails[k] = f
		}
		f.count++
		f.last = now
		if over := f.count - freeTries(k); over > 0 {
			lock := loginFirstLock << min(over-1, 10)
			maxLock := loginMaxLock
			if strings.HasPrefix(k, "peer ") {
				maxLock = loginPeerMaxLock
			}
			if lock > maxLock {
				lock = maxLock
			}
			f.until = now.Add(lock)
		}
	}
}

func (s *Server) loginSucceeded(keys []string) {
	s.loginMu.Lock()
	for _, k := range keys {
		delete(s.loginFails, k)
	}
	s.loginMu.Unlock()
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps post-login redirects on this site (a local path), defaulting to /admin. It
// must reject anything that resolves off-site: a protocol-relative "//host"; any backslash,
// because browsers fold "\" into "/" per the URL spec (so "/\host" can't sneak through as
// "//host"); and any control character or space, because browsers strip tabs and newlines from a
// URL (so "/\t/host" would become "//host"). A legitimate in-app path has none of them.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsRune(next, '\\') {
		return "/admin"
	}
	for _, r := range next {
		if r <= ' ' || r == 0x7f {
			return "/admin"
		}
	}
	return next
}

// isHTTPS reports whether the browser reached us over TLS, so we only flag the cookie
// Secure then. Caddy terminates TLS and proxies in plain HTTP, so trust its forwarded
// header; direct http://host:port access (no TLS) keeps the cookie usable.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

type serviceLink struct{ Name, Icon, Desc, URL string }

type homeData struct {
	Cfg      config.Settings
	Services []serviceLink
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// Fresh install (hsctl install ran, nothing configured yet): the home page has nothing to
	// show, so take the admin straight into the wizard.
	if !s.Provision.Configured() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	c := s.Settings.Loaded()
	var links []serviceLink
	for _, svc := range catalog.Load(s.Repo.Path(paths.Services)) {
		if svc.Dir != "" && c.IsDisabled(svc.Dir) {
			continue
		}
		links = append(links, serviceLink{svc.Name, svc.Icon, svc.Desc, svc.URL(c.ServerIP)})
	}
	render(w, homeTmpl, homeData{Cfg: c, Services: links})
}

type helpData struct{ Body template.HTML }

// handleHelp renders ONBOARDING.md (with SERVER_IP filled in) as an in-dashboard guide.
func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	c := s.Settings.Loaded()
	md, err := os.ReadFile(s.Repo.Path(paths.Onboarding))
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
	Cfg        config.Settings
	Containers []stack.Container
	DockerErr  string
	Flash      flash
	Sys        sysinfo.Stats
	Backup     backup.Freshness
	SetupDone  bool
	Busy       string // what's running right now, "" if nothing
	JobID      string // the dashboard job to follow, if one is running
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	d := adminData{Cfg: s.Settings.Loaded(), Flash: takeFlash(w, r), Backup: s.Backup.Freshness(), SetupDone: s.Provision.Configured()}
	d.Busy, d.JobID = s.busyNote()
	// gatherSysStats blocks ~300ms for its CPU sample, so overlap it with docker ps.
	sysCh := make(chan sysinfo.Stats, 1)
	go func() { sysCh <- s.Sysinfo.Gather() }()
	st, err := s.Stack.Containers()
	if err != nil {
		s.Log.Warn("docker unreachable", "err", err)
		if d.DockerErr = s.Docker.Problem(); d.DockerErr == "" {
			d.DockerErr = "Couldn't list the containers: " + err.Error()
		}
	}
	d.Containers = st
	d.Sys = <-sysCh
	render(w, adminTmpl, d)
}

// handleAction powers the machine off. Start/Stop/Restart on the admin page are Command Center
// jobs (up / down --keep-proxy / restart), so they run to the end even if the page is closed.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("do") != "shutdown" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	// Powering off must not cut a backup or restore short.
	l, err := s.Lock.Acquire("dashboard: shutdown", 0)
	if err != nil {
		redirectFlash(w, r, "/admin", err.Error(), true)
		return
	}
	defer l.Release()
	msg := s.runShutdown()
	s.Log.Info("admin action", "do", "shutdown", "from", remoteIP(r))
	redirectFlash(w, r, "/admin", msg, false)
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
func (s *Server) runShutdown() string {
	go func() {
		time.Sleep(2 * time.Second)
		s.Log.Warn("powering off the machine (dashboard shutdown button)")
		_ = s.Priv.Command("shutdown", "-h", "now").Run()
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
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
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
	Rows      []devices.Row
	Err       string
	Flash     flash
	MountAt   string // where mounts land (/mnt), shown to the user
	GuardPath string // configured backup REQUIRE_MOUNT path, "" if none
	GuardOK   bool   // is that path a real mount right now
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	cfg := s.Backup.Settings()
	d := devicesData{Flash: takeFlash(w, r), MountAt: devices.MountRoot, GuardPath: cfg.RequireMount}
	if cfg.RequireMount != "" {
		d.GuardOK = s.Backup.RequireMount(cfg) == nil // is the backup disk already mounted there?
	}
	devs, err := s.Devices.List()
	if err != nil {
		d.Err = err.Error()
	} else {
		d.Rows = devices.Rows(devs)
	}
	render(w, devicesTmpl, d)
}

// handleDeviceMount mounts the posted device at the directory the operator chose in the form
// (the same freedom as a manual `mount <disk> <dir>`). A blank target falls back to the
// per-label default under /mnt; mountDeviceAt validates both the device and the directory.
func (s *Server) handleDeviceMount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
		return
	}
	if holder := s.Lock.Busy(); holder != "" {
		redirectFlash(w, r, "/admin/devices", "Not now — "+holder+" is running and may be using the disks. Try again when it's finished.", true)
		return
	}
	target := strings.TrimSpace(r.FormValue("target"))
	dev := strings.TrimSpace(r.FormValue("dev"))
	if mp, err := s.Devices.Mount(dev, target); err != nil {
		s.Log.Warn("mount failed", "dev", dev, "target", target, "err", err)
		redirectFlash(w, r, "/admin/devices", "Mount failed: "+err.Error(), true)
	} else {
		s.Log.Info("mounted device", "dev", dev, "at", mp, "from", remoteIP(r))
		redirectFlash(w, r, "/admin/devices", "Mounted at "+mp, false)
	}
}

// deviceActionHandler builds the POST handler for a device action (currently Eject): run it
// on the posted device, then flash the result back on /admin/devices.
func (s *Server) deviceActionHandler(action func(string) (string, error), errPrefix string, ok func(string) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
			return
		}
		// Ejecting the backup disk between restic's writes would send the rest of a backup onto
		// the system disk (or fail a restore half-way).
		if holder := s.Lock.Busy(); holder != "" {
			redirectFlash(w, r, "/admin/devices", "Not now — "+holder+" is running and may be using the disks. Try again when it's finished.", true)
			return
		}
		dev := strings.TrimSpace(r.FormValue("dev"))
		if res, err := action(dev); err != nil {
			s.Log.Warn("device action failed", "dev", dev, "err", err)
			redirectFlash(w, r, "/admin/devices", errPrefix+err.Error(), true)
		} else {
			s.Log.Info("device action ok", "dev", dev, "result", res, "from", remoteIP(r))
			redirectFlash(w, r, "/admin/devices", ok(res), false)
		}
	}
}

// ---- Terminal page (the WebSocket handler lives in terminal.go) --------------

func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	render(w, terminalTmpl, nil)
}

type backupData struct {
	Repo, Retention, Snapshots string
	Flash                      flash
	Replica                    string // off-site replica repo, "" if unset
	ResticOK                   bool
	ResticVersion              string
	GuardPath                  string // REQUIRE_MOUNT path, "" if unset
	GuardOK                    bool   // is that path a real mount right now
	Stats                      string // restic repo stats, "" if unavailable
	Busy, JobID                string
	KeepDaily, KeepWeekly      string // the retention form's fields ("" = not set)
	KeepMonthly, KeepRest      string
	RetentionErr               string // the saved policy is invalid (nothing gets pruned)
	Notify                     string // failure-notification URL, "" if unset
	Last                       backup.Freshness
	Timer                      backup.Timer
	SystemDisk                 string // the repo is on the same disk as this (no real protection)
	ReplicaAge                 string // when the off-site replica last got a copy, "" if never
	HasPassword                bool   // .restic-password exists
	GuardWarn                  string // the repo isn't on the guarded disk
	Images                     bool   // every backup saves the app images themselves (BACKUP_IMAGES)
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	cfg := s.Backup.Settings()
	d := backupData{Repo: cfg.Repo, Retention: cfg.Retention, Replica: cfg.ReplicaRepo,
		ResticOK: s.Restic.Installed(), Flash: takeFlash(w, r), GuardPath: cfg.RequireMount,
		Notify: cfg.NotifyURL, Last: s.Backup.Freshness(), Timer: s.Backup.Timer(),
		ReplicaAge: s.Backup.ReplicaAge(), HasPassword: fsutil.Exists(s.Repo.Path(paths.ResticPassword)), Images: cfg.Images}
	if cfg.RequireMount != "" {
		d.GuardOK = s.Backup.RequireMount(cfg) == nil // is the backup disk actually mounted now?
	}
	if v, err := s.Restic.Version(); err == nil {
		d.ResticVersion = v
	}
	d.Busy, d.JobID = s.busyNote()
	if _, err := backup.ParseRetention(cfg.Retention); err != nil {
		d.RetentionErr = err.Error()
	}
	daily, weekly, monthly, rest := backup.RetentionKeeps(cfg.Retention)
	field := func(n int) string {
		if n == -2 { // not in the policy
			return ""
		}
		return strconv.Itoa(n) // -1 is restic's "keep all of them"
	}
	d.KeepDaily, d.KeepWeekly, d.KeepMonthly, d.KeepRest = field(daily), field(weekly), field(monthly), rest
	if p := backup.LocalRepoPath(cfg.Repo); p != "" && cfg.RequireMount == "" {
		if same, on := s.Backup.OnSystemDisk(p); same {
			d.SystemDisk = on
		}
	}
	d.GuardWarn = backup.GuardMismatch(cfg)
	switch {
	case !d.ResticOK:
	case s.Backup.DiskMissing(cfg) != "":
		// Don't ask restic: it would only say "repository does not exist", which reads like
		// "initialize a new one" — the wrong move while the disk is merely unplugged.
		d.Snapshots = "The backup disk isn't mounted at " + cfg.RequireMount +
			", so the snapshots can't be read. Mount it on the Drives page."
	case s.Backup.RequirePassword() != nil:
		d.Snapshots = s.Backup.RequirePassword().Error()
	default:
		if out, err := s.Backup.Snapshots(); err != nil {
			if restic.RepoMissing(out) {
				d.Snapshots = "There's no backup repository at " + cfg.Repo + " yet — click Initialize below."
			} else {
				d.Snapshots = "Couldn't read the snapshots:\n\n" + out
			}
		} else {
			d.Snapshots = out
		}
		if out, err := s.Backup.Stats(); err == nil {
			d.Stats = strings.TrimSpace(out)
		}
	}
	render(w, backupTmpl, d)
}

func (s *Server) handleBackupConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/backup", http.StatusSeeOther)
		return
	}
	msg, isErr := "Saved.", false
	cfg := s.Backup.Settings()
	_ = r.ParseForm()
	// The guard box shows the current value, so an emptied box turns the guard off — unless the
	// destination just moved to an external-disk mountpoint, which gets the guard automatically.
	guardEdited := false
	if r.Form.Has("require_mount") {
		v := strings.TrimSpace(r.FormValue("require_mount"))
		guardEdited = v != cfg.RequireMount
		cfg.RequireMount = v
	}
	if v := strings.TrimSpace(r.FormValue("repo")); v != "" && v != cfg.Repo {
		old := cfg.Repo
		cfg.Repo = v
		if !guardEdited {
			if g, changed := backup.FollowGuard(cfg.RequireMount, old, v); changed {
				cfg.RequireMount = g
				if g != "" {
					msg = "Saved. Backups now refuse to run unless a disk is mounted at " + g + " (the disk guard)."
				}
			}
		}
	}
	if r.Form.Has("keep_daily") {
		policy, err := backup.BuildRetention(r.FormValue("keep_daily"), r.FormValue("keep_weekly"),
			r.FormValue("keep_monthly"), r.FormValue("keep_rest"))
		if err != nil {
			redirectFlash(w, r, "/admin/backup", "Not saved — "+err.Error(), true)
			return
		}
		cfg.Retention = policy
	}
	if r.Form.Has("replica") {
		cfg.ReplicaRepo = strings.TrimSpace(r.FormValue("replica")) // an empty box clears it
	}
	// An unticked box sends nothing, so the form says it showed the box.
	if r.Form.Has("images_shown") {
		cfg.Images = r.FormValue("images") != ""
	}
	if r.Form.Has("notify") {
		v := strings.TrimSpace(r.FormValue("notify"))
		if v != "" && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
			redirectFlash(w, r, "/admin/backup", "Not saved — the notification address must be an http(s):// URL", true)
			return
		}
		cfg.NotifyURL = v
	}
	if w := backup.GuardMismatch(cfg); w != "" {
		msg += " Warning: " + w + "."
	}
	if err := s.Backup.SaveSettings(cfg); err != nil {
		msg, isErr = "Save failed: "+err.Error(), true
	}
	redirectFlash(w, r, "/admin/backup", msg, isErr)
}

// handleBackupPassword sets or changes the backup password (see setResticPassword): it puts back a
// saved password, sets one before Initialize, or changes the repository's own password — never
// just overwrites the file under existing backups. It takes the operation lock, so it can't run
// under a backup.
func (s *Server) handleBackupPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/backup", http.StatusSeeOther)
		return
	}
	pw := r.FormValue("password")
	if pw != r.FormValue("confirm") {
		redirectFlash(w, r, "/admin/backup", "Not saved — the two passwords don't match.", true)
		return
	}
	lock, err := s.Lock.Acquire("dashboard: backup password", 0)
	if err != nil {
		redirectFlash(w, r, "/admin/backup", "Not saved — "+err.Error(), true)
		return
	}
	defer lock.Release()
	var out strings.Builder
	if err := s.Backup.SetPassword(s.Backup.Settings(), pw, &out); err != nil {
		s.Log.Warn("backup password not set", "err", err, "from", remoteIP(r))
		redirectFlash(w, r, "/admin/backup", "Not saved — "+err.Error(), true)
		return
	}
	s.Log.Info("backup password set", "from", remoteIP(r))
	redirectFlash(w, r, "/admin/backup", strings.TrimSpace(strings.ReplaceAll(out.String(), "\n", " ")), false)
}

type restoreData struct {
	Snapshots string
	Flash     flash
	ResticOK  bool
}

// handleBackupRestore shows a confirmation page (GET) and starts the destructive DR put-back
// (POST) — but only when the operator types RESTORE. The put-back is `hsctl backup restore
// --into-volumes` run as a dashboard job: it extracts the snapshot, checks it, stops the stack,
// repopulates every volume (Vaultwarden from its staged copy) and brings the stack back up. The
// POST redirects to a progress page that follows the job, so a reload can't start a second
// restore, and the job runs to the end even while Caddy — this page's only route — restarts.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if strings.TrimSpace(r.FormValue("confirm")) != "RESTORE" {
			redirectFlash(w, r, "/admin/backup/restore", "Type RESTORE to confirm — nothing was changed.", true)
			return
		}
		args := []string{"backup", "restore", "--into-volumes", "--yes"}
		if r.FormValue("versions") == "current" {
			args = append(args, "--current-images")
		}
		snap := strings.TrimSpace(r.FormValue("snapshot"))
		if snap != "" {
			if strings.HasPrefix(snap, "-") || strings.ContainsAny(snap, " \t/") {
				redirectFlash(w, r, "/admin/backup/restore", "That doesn't look like a snapshot ID — nothing was changed.", true)
				return
			}
			args = append(args, snap)
		}
		s.Log.Warn("restore requested", "snapshot", snap, "from", remoteIP(r))
		if holder := s.Lock.Busy(); holder != "" {
			redirectFlash(w, r, "/admin/backup/restore", "Another operation is running ("+holder+") — try again once it finishes.", true)
			return
		}
		j, err := s.Jobs.Start("restore", "Restore from backup", args)
		if err != nil {
			redirectFlash(w, r, "/admin/backup/restore", err.Error(), true)
			return
		}
		http.Redirect(w, r, "/admin/backup/restore/progress?id="+url.QueryEscape(j.ID), http.StatusSeeOther)
		return
	}
	d := restoreData{Flash: takeFlash(w, r), ResticOK: s.Restic.Installed()}
	if d.ResticOK {
		if out, err := s.Backup.Snapshots(); err == nil {
			d.Snapshots = out
		}
	}
	render(w, restoreTmpl, d)
}

// handleBackupRestoreProgress is the page a restore redirects to: it follows the restore job's
// output, riding out the minutes the proxy is down mid-restore.
func (s *Server) handleBackupRestoreProgress(w http.ResponseWriter, r *http.Request) {
	render(w, restoreProgressTmpl, struct{ JobID string }{r.URL.Query().Get("id")})
}

// handleCert serves the public root CA (from the saved file, else extracted live).
func (s *Server) handleCert(w http.ResponseWriter, r *http.Request) {
	if b, err := os.ReadFile(s.Repo.Path(paths.CACert)); err == nil {
		serveCert(w, b)
		return
	}
	if b, err := s.Stack.CA(); err == nil {
		serveCert(w, b)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	render(w, certMissingTmpl, nil)
}

func serveCert(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="homeserver-ca.crt"`)
	w.Write(b)
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
		slog.Error("template parse failed", "err", err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		slog.Error("template render failed", "err", err)
		http.Error(w, err.Error(), 500)
	}
}
