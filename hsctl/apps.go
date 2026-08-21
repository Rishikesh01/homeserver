package main

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// Apps can be switched on and off — at first setup (the wizard's checkboxes) and later
// (the Apps admin page, or `hsctl apps enable|disable`). "Off" means: `hsctl up` skips the
// app's compose project, its containers are stopped, and its tile disappears from the home
// page. Its data volumes and .env are KEPT, so switching it back on is lossless. Caddy is
// never an app here — it's the front door for whatever is on.
//
// The switch is a list of compose dir names in setup.conf (DISABLED_APPS=stirling,it-tools),
// so it survives re-setup and rides along in backups like every other setting.

// appDirs are the switchable compose projects, in start order (everything but Caddy).
func appDirs() []string {
	var out []string
	for _, s := range services {
		if s != "caddy" {
			out = append(out, s)
		}
	}
	return out
}

// IsDisabled reports whether the app (compose dir name) is switched off.
func (c Config) IsDisabled(dir string) bool {
	for _, d := range c.DisabledApps {
		if d == dir {
			return true
		}
	}
	return false
}

// setDisabled adds or removes dir from DisabledApps, keeping the list in start order.
func (c *Config) setDisabled(dir string, disabled bool) {
	set := map[string]bool{}
	for _, d := range c.DisabledApps {
		set[d] = true
	}
	set[dir] = disabled
	c.DisabledApps = nil
	for _, d := range appDirs() {
		if set[d] {
			c.DisabledApps = append(c.DisabledApps, d)
		}
	}
}

// enabledServices is the start order with disabled apps removed (Caddy always included).
func enabledServices(c Config) []string {
	var out []string
	for _, s := range services {
		if s == "caddy" || !c.IsDisabled(s) {
			out = append(out, s)
		}
	}
	return out
}

// appInfo is one row for the wizard / Apps page / CLI: the compose dir plus the friendly
// name and blurb from services.json (falling back to the dir name for an app without a tile).
type appInfo struct {
	Dir, Name, Icon, Desc string
	Enabled               bool
	Running               bool // any container of the compose project is up (best-effort)
}

func listApps(repo string, c Config, withState bool) []appInfo {
	tiles := map[string]Service{}
	for _, t := range LoadServices(repo) {
		if t.Dir != "" {
			tiles[t.Dir] = t
		}
	}
	var out []appInfo
	for _, d := range appDirs() {
		a := appInfo{Dir: d, Name: d, Icon: "📦", Enabled: !c.IsDisabled(d)}
		if t, ok := tiles[d]; ok {
			a.Name, a.Icon, a.Desc = t.Name, t.Icon, t.Desc
		}
		if withState {
			a.Running = composeRunning(repo, d)
		}
		out = append(out, a)
	}
	return out
}

// composeRunning reports whether the compose project in dir has any running container.
func composeRunning(repo, dir string) bool {
	out, err := dockerOut(filepath.Join(repo, dir), "compose", "ps", "-q", "--status", "running")
	return err == nil && strings.TrimSpace(out) != ""
}

// validApp checks dir names arriving from the browser / CLI against the registry.
func validApp(dir string) bool {
	for _, d := range appDirs() {
		if d == dir {
			return true
		}
	}
	return false
}

// setAppEnabled flips the switch, saves setup.conf, and applies it to the running stack:
// enable = compose up -d (after the edge network exists), disable = compose down (volumes
// kept). Progress goes to w. The config is saved first so a docker hiccup never leaves the
// saved state and the intended state disagreeing for the next `hsctl up`.
func setAppEnabled(repo, dir string, enabled bool, w io.Writer) error {
	if !validApp(dir) {
		return fmt.Errorf("unknown app %q — one of: %s", dir, strings.Join(appDirs(), ", "))
	}
	c := LoadConfig(repo)
	c.Normalize()
	c.setDisabled(dir, !enabled)
	if err := c.Save(repo); err != nil {
		return err
	}
	if enabled {
		if m := missingEnvIn(repo); len(m) > 0 {
			for _, s := range m {
				if s == dir {
					return fmt.Errorf("%s has no .env yet — run the setup wizard (or `hsctl setup`) first", dir)
				}
			}
		}
		if err := ensureEdgeNetwork(); err != nil {
			return err
		}
		fmt.Fprintf(w, "== enabling %s: starting ==\n", dir)
		if out, err := dockerCombined(filepath.Join(repo, dir), "compose", "up", "-d"); err != nil {
			return fmt.Errorf("%s: %v\n%s", dir, err, strings.TrimSpace(out))
		}
		fmt.Fprintf(w, "%s is on.\n", dir)
		return nil
	}
	fmt.Fprintf(w, "== disabling %s: stopping (data kept) ==\n", dir)
	if out, err := dockerCombined(filepath.Join(repo, dir), "compose", "down"); err != nil {
		return fmt.Errorf("%s: %v\n%s", dir, err, strings.TrimSpace(out))
	}
	fmt.Fprintf(w, "%s is off. Its data is kept; enable it again any time.\n", dir)
	return nil
}

// ---- CLI: hsctl apps [enable|disable NAME] ----

func appsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "apps", Short: "List apps and switch them on/off (data is kept)",
		Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			c := LoadConfig(repo)
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "APP\tSTATE\tRUNNING\tNAME")
			for _, a := range listApps(repo, c, true) {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Dir, boolStr(a.Enabled, "enabled", "disabled"), boolStr(a.Running, "yes", "no"), a.Name)
			}
			return tw.Flush()
		}}
	for _, v := range []struct {
		use, short string
		on         bool
	}{
		{"enable NAME", "Switch an app on and start it", true},
		{"disable NAME", "Switch an app off and stop it (data volumes are kept)", false},
	} {
		on := v.on
		cmd.AddCommand(&cobra.Command{Use: v.use, Short: v.short, Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				repo, err := requireRepoDir()
				if err != nil {
					return err
				}
				return setAppEnabled(repo, args[0], on, os.Stdout)
			}})
	}
	return cmd
}

// ---- dashboard: /admin/apps ----

type appsData struct {
	Apps []appInfo
	Msg  string
	Err  string
}

func (s *uiServer) handleApps(w http.ResponseWriter, r *http.Request) {
	render(w, appsTmpl, appsData{Apps: listApps(s.repo, s.config(), true), Msg: r.URL.Query().Get("msg"), Err: r.URL.Query().Get("err")})
}

// handleAppsToggle flips one app. POST-only with a fixed action vocabulary; the dir is
// checked against the registry before anything is exec'd.
func (s *uiServer) handleAppsToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/apps", http.StatusSeeOther)
		return
	}
	dir, action := r.FormValue("app"), r.FormValue("action")
	if !validApp(dir) || (action != "enable" && action != "disable") {
		http.Error(w, "unknown app or action", http.StatusBadRequest)
		return
	}
	uiLog.Info("app toggle", "app", dir, "action", action, "from", remoteIP(r))
	var out strings.Builder
	if err := setAppEnabled(s.repo, dir, action == "enable", &out); err != nil {
		http.Redirect(w, r, "/admin/apps?err="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/apps?msg="+template.URLQueryEscaper(strings.TrimSpace(lastLine(out.String()))), http.StatusSeeOther)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

const appsTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Apps</title>
<style>{{css}}
.app{display:grid;grid-template-columns:auto 1fr auto;gap:14px;align-items:center;background:var(--card);border:1px solid #262b34;border-radius:12px;padding:14px 16px;margin:0 0 10px}
.app .ico{font-size:26px}.app h4{margin:0;font-size:16px}.app p{margin:2px 0 0;color:var(--muted);font-size:14px}
.app.off{opacity:.7}
</style></head><body><div class="wrap">
<h1>🧩 Apps</h1>
<p class="sub">Switch an app off to stop it and hide it from the home page. Its data is kept, so switching it back on is instant and lossless.</p>
{{if .Msg}}<div class="flash">{{.Msg}}</div>{{end}}
{{if .Err}}<div class="banner">{{.Err}}</div>{{end}}
{{range .Apps}}<div class="app{{if not .Enabled}} off{{end}}">
  <div class="ico">{{.Icon}}</div>
  <div><h4>{{.Name}} <span class="tag {{if .Running}}ok{{else}}bad{{end}}">{{if .Running}}running{{else}}stopped{{end}}</span>{{if not .Enabled}}<span class="tag">off</span>{{end}}</h4>
    <p>{{.Desc}}{{if not .Desc}}{{.Dir}}{{end}}</p></div>
  <form method="post" action="/admin/apps/toggle">
    <input type="hidden" name="app" value="{{.Dir}}">
    {{if .Enabled}}<input type="hidden" name="action" value="disable"><button class="btn gray" onclick="return confirm('Switch off {{.Name}}? It stops now and disappears from the home page. Your data is kept.')">Switch off</button>
    {{else}}<input type="hidden" name="action" value="enable"><button class="btn green">Switch on</button>{{end}}
  </form>
</div>{{end}}
<p class="foot">Anyone who opens a switched-off app's address sees a “this app isn't running” page that points back here. Caddy (the HTTPS front door) and this dashboard can't be switched off. To remove an app for good, including its data, use the terminal: <code>hsctl apps disable NAME</code> then <code>docker compose -f NAME/docker-compose.yml down -v</code>.</p>
<p class="foot"><a href="/admin">← Admin</a></p>
</div></body></html>`
