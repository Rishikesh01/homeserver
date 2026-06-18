package main

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// markdown renders with GFM enabled (tables, autolinks, strikethrough).
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

type uiServer struct {
	repo string
	pass string
}

func runUI(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	s := &uiServer{repo: repoDir(), pass: uiPassword(repoDir())}
	c := LoadConfig(s.repo)
	c.Normalize()
	if addr == "" {
		addr = fmt.Sprintf(":%d", c.UIPort)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/help", s.handleHelp)
	mux.HandleFunc("/root.crt", s.handleCert)
	mux.HandleFunc("/admin", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("/admin/action", s.requireAuth(s.handleAction))
	mux.HandleFunc("/admin/backup", s.requireAuth(s.handleBackup))
	mux.HandleFunc("/admin/backup/run", s.requireAuth(s.handleBackupRun))
	mux.HandleFunc("/admin/backup/config", s.requireAuth(s.handleBackupConfig))
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

func (s *uiServer) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte("admin")) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(s.pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="hsctl admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
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
