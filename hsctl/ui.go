package main

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
}

func runUI(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	s := &uiServer{repo: repoDir(), pass: uiPassword(repoDir()), sessions: map[string]time.Time{}}
	c := LoadConfig(s.repo)
	c.Normalize()
	if addr == "" {
		addr = fmt.Sprintf(":%d", c.UIPort)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/help", s.handleHelp)
	mux.HandleFunc("/root.crt", s.handleCert)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/admin", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/admin/action", s.requireAuth(s.handleAction))
	mux.HandleFunc("/admin/backup", s.requireAuth(s.handleBackup))
	mux.HandleFunc("/admin/backup/run", s.requireAuth(s.handleBackupRun))
	mux.HandleFunc("/admin/backup/config", s.requireAuth(s.handleBackupConfig))
	mux.HandleFunc("/admin/backup/restore", s.requireAuth(s.handleBackupRestore))
	port := portOf(addr)
	fmt.Printf("hsctl ui listening on %s\n", addr)
	fmt.Printf("  dashboard : https://%s/   (via Caddy)   ·   http://%s%s/   (direct)\n", c.ServerIP, c.ServerIP, port)
	fmt.Printf("  admin     : https://%s/admin   ·   http://%s%s/admin\n", c.ServerIP, c.ServerIP, port)
	fmt.Printf("  login     : user 'admin', password in %s\n", filepath.Join(s.repo, ".ui-password"))
	return http.ListenAndServe(addr, mux)
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return addr
}

// uiPassword returns the admin password from $HSCTL_UI_PASSWORD or .ui-password,
// generating and saving one on first run.
func uiPassword(repo string) string {
	if p := os.Getenv("HSCTL_UI_PASSWORD"); p != "" {
		return p
	}
	path := filepath.Join(repo, ".ui-password")
	if b, err := os.ReadFile(path); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	p := genPassword(16)
	_ = writeFile0600(path, p+"\n")
	return p
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
			w.WriteHeader(http.StatusUnauthorized)
			render(w, loginTmpl, loginData{Err: "Wrong username or password.", Next: safeNext(r.FormValue("next"))})
			return
		}
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

// safeNext keeps post-login redirects on this site (a local path), defaulting to /admin.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
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
	c := LoadConfig(s.repo)
	c.Normalize()
	var links []serviceLink
	for _, svc := range LoadServices(s.repo) {
		links = append(links, serviceLink{svc.Name, svc.Icon, svc.Desc, svc.URL(c.ServerIP)})
	}
	render(w, homeTmpl, homeData{Cfg: c, Services: links})
}

type helpData struct{ Body template.HTML }

// handleHelp renders ONBOARDING.md (with SERVER_IP filled in) as an in-dashboard guide.
func (s *uiServer) handleHelp(w http.ResponseWriter, r *http.Request) {
	c := LoadConfig(s.repo)
	c.Normalize()
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
}

func (s *uiServer) handleAdmin(w http.ResponseWriter, r *http.Request) {
	c := LoadConfig(s.repo)
	c.Normalize()
	d := adminData{Cfg: c, Msg: r.URL.Query().Get("msg")}
	st, err := s.status()
	if err != nil {
		d.DockerErr = "Docker is not reachable. Is the daemon running, and is this user in the 'docker' group? (sudo usermod -aG docker $USER, then re-login)"
	}
	d.Containers = st
	render(w, adminTmpl, d)
}

func (s *uiServer) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	var msg string
	switch r.FormValue("do") {
	case "up":
		msg = s.runLifecycle("up")
	case "down":
		msg = s.runLifecycle("down")
	case "restart":
		msg = s.runLifecycle("down") + " " + s.runLifecycle("up")
	case "shutdown":
		msg = s.runShutdown()
	default:
		msg = "unknown action"
	}
	http.Redirect(w, r, "/admin?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}

// runLifecycle performs up/down across all services and returns a short summary.
func (s *uiServer) runLifecycle(action string) string {
	order := services
	cargs := []string{"compose", "up", "-d"}
	if action == "down" {
		cargs = []string{"compose", "down"}
		order = reversed(services)
	}
	var failed []string
	for _, svc := range order {
		if _, err := dockerCombined(filepath.Join(s.repo, svc), cargs...); err != nil {
			failed = append(failed, svc)
		}
	}
	if len(failed) > 0 {
		return fmt.Sprintf("%s: FAILED for %s", action, strings.Join(failed, ", "))
	}
	return action + ": ok"
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
		_ = shutdownCmd().Run()
	}()
	return "Shutting down — the server is powering off now. This page will go offline. " +
		"When you switch the machine back on, the apps start again automatically."
}

// shutdownCmd builds the poweroff command, using sudo when the UI isn't running as root
// (under systemd it runs as root; a local dev run may not).
func shutdownCmd() *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command("shutdown", "-h", "now")
	}
	return exec.Command("sudo", "shutdown", "-h", "now")
}

type backupData struct {
	Repo, Retention, Snapshots, Msg string
	ResticOK                        bool
}

func (s *uiServer) handleBackup(w http.ResponseWriter, r *http.Request) {
	cfg := loadBackupCfg(s.repo)
	d := backupData{Repo: cfg.Repo, Retention: cfg.Retention, ResticOK: resticInstalled(),
		Msg: r.URL.Query().Get("msg")}
	if d.ResticOK {
		if out, err := resticOutput(s.repo, cfg, "snapshots"); err != nil {
			d.Snapshots = "No snapshots yet — set a destination, then 'Initialize', then 'Back up now'.\n\n" + out
		} else {
			d.Snapshots = out
		}
	}
	render(w, backupTmpl, d)
}

func (s *uiServer) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/backup", http.StatusSeeOther)
		return
	}
	msg := "Backup complete."
	if err := backupRun(s.repo, loadBackupCfg(s.repo)); err != nil {
		msg = "Backup failed: " + err.Error()
	}
	http.Redirect(w, r, "/admin/backup?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}

func (s *uiServer) handleBackupConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		cfg := loadBackupCfg(s.repo)
		if v := strings.TrimSpace(r.FormValue("repo")); v != "" {
			cfg.Repo = v
		}
		if v := strings.TrimSpace(r.FormValue("retention")); v != "" {
			cfg.Retention = v
		}
		_ = cfg.save(s.repo)
	}
	http.Redirect(w, r, "/admin/backup?msg=Destination+saved", http.StatusSeeOther)
}

type restoreData struct {
	Snapshots, Msg string
	ResticOK       bool
}

// handleBackupRestore shows a confirmation page (GET) and runs the destructive DR put-back
// (POST) — but only when the operator types RESTORE. It stops the stack, repopulates every
// volume from the snapshot (Vaultwarden from its staged copy), and brings the stack back up.
// The run is synchronous so the operator sees the real result; note it restarts Caddy, so
// prefer the server's direct http address over the https one (see the page's warning).
func (s *uiServer) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	cfg := loadBackupCfg(s.repo)
	if r.Method == http.MethodPost {
		if strings.TrimSpace(r.FormValue("confirm")) != "RESTORE" {
			http.Redirect(w, r, "/admin/backup/restore?msg="+
				template.URLQueryEscaper("Type RESTORE to confirm — nothing was changed."), http.StatusSeeOther)
			return
		}
		msg := "Restore complete — all services were brought back up."
		if err := restoreSnapshotIntoVolumes(s.repo, cfg, strings.TrimSpace(r.FormValue("snapshot"))); err != nil {
			msg = "Restore FAILED: " + err.Error()
		}
		http.Redirect(w, r, "/admin/backup?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
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
}

func render(w http.ResponseWriter, tmpl string, data any) {
	t, err := template.New("p").Funcs(tmplFuncs).Parse(tmpl)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}
