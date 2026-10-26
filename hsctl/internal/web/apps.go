package web

import (
	"net/http"

	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
)

type appsData struct {
	Apps        []stack.App
	Busy, JobID string
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	d := appsData{Apps: s.Stack.Apps(true)}
	d.Busy, d.JobID = s.busyNote()
	render(w, appsTmpl, d)
}

// handleAppsToggle flips one app as a dashboard job (`hsctl apps enable|disable NAME`), so a
// slow image download can't be cut short by the page closing. POST-only with a fixed action
// vocabulary; the dir is checked against the registry before anything is exec'd.
func (s *Server) handleAppsToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/apps", http.StatusSeeOther)
		return
	}
	dir, action := r.FormValue("app"), r.FormValue("action")
	if !layout.IsApp(dir) || (action != "enable" && action != "disable") {
		http.Error(w, "unknown app or action", http.StatusBadRequest)
		return
	}
	title := map[string]string{"enable": "Switch on ", "disable": "Switch off "}[action] + dir
	if j, ok := s.startJob(w, r, "apps-"+action, title, []string{"apps", action, dir}); ok {
		writeJobID(w, j)
	}
}

const appsTmpl = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Apps</title>
<style>{{css}}
.app{display:grid;grid-template-columns:auto 1fr auto;gap:14px;align-items:center;background:var(--card);border:1px solid #262b34;border-radius:12px;padding:14px 16px;margin:0 0 10px}
.app .ico{font-size:26px}.app h4{margin:0;font-size:16px}.app p{margin:2px 0 0;color:var(--muted);font-size:14px}
.app.off{opacity:.7}
</style></head><body><div class="wrap">
<h1>🧩 Apps</h1>
<p class="sub">Switch an app off to stop it and hide it from the home page. Its data is kept, so switching it back on is instant and lossless.</p>
{{if .Busy}}<div class="note" role="status">⏳ Running now: {{.Busy}}</div>{{end}}
{{range .Apps}}<div class="app{{if not .Enabled}} off{{end}}">
  <div class="ico">{{.Icon}}</div>
  <div><h4>{{.Name}} <span class="tag {{if .Running}}ok{{else}}bad{{end}}">{{if .Running}}running{{else}}stopped{{end}}</span>{{if not .Enabled}}<span class="tag">off</span>{{end}}</h4>
    <p>{{.Desc}}{{if not .Desc}}{{.Dir}}{{end}}</p></div>
  <div>{{if .Enabled}}<button class="btn gray" data-post="/admin/apps/toggle" data-body="app={{.Dir}}&amp;action=disable" data-reload="1" data-confirm="Switch off {{.Name}}? It stops now and disappears from the home page. Your data is kept.">Switch off</button>
    {{else}}<button class="btn green" data-post="/admin/apps/toggle" data-body="app={{.Dir}}&amp;action=enable" data-reload="1" data-confirm="">Switch on</button>{{end}}</div>
</div>{{end}}
<p id="jobstatus" class="foot" aria-live="polite"></p>
<div id="out" class="out" role="log" aria-live="polite" data-job="{{.JobID}}" data-reload="1" hidden></div>
<p class="foot">Anyone who opens a switched-off app's address sees a “this app isn't running” page that points back here. Caddy (the HTTPS front door) and this dashboard can't be switched off. To remove an app for good, including its data, use the terminal: <code>hsctl apps disable NAME</code> then <code>docker compose -f NAME/docker-compose.yml down -v</code>.</p>
<p class="foot"><a href="/admin">← Admin</a> · <a href="/logout">Log out</a></p>
<script>` + runJS + `</script>
</div></body></html>`
