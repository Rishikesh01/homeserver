package main

const cssText = `
:root{--bg:#0f1115;--card:#1a1d24;--fg:#e7e9ee;--muted:#9aa3b2;--accent:#4f8cff;--ok:#3fb950;--bad:#f85149}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
.wrap{max-width:860px;margin:0 auto;padding:28px 18px}
h1{font-size:26px;margin:0 0 4px}.sub{color:var(--muted);margin:0 0 24px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:14px}
.card{background:var(--card);border:1px solid #262b34;border-radius:12px;padding:18px;text-decoration:none;color:inherit;display:block}
.card:hover{border-color:var(--accent)}.card h3{margin:0 0 6px;font-size:18px}.card p{margin:0;color:var(--muted);font-size:14px}
.tag{display:inline-block;font-size:12px;padding:1px 8px;border-radius:99px;margin-left:6px}
.tag.ok{background:rgba(63,185,80,.15);color:var(--ok)}.tag.bad{background:rgba(248,81,73,.15);color:var(--bad)}
table{width:100%;border-collapse:collapse;margin:8px 0 18px}td,th{text-align:left;padding:8px 6px;border-bottom:1px solid #262b34}
.btn{background:var(--accent);color:#fff;border:0;border-radius:8px;padding:9px 16px;font-size:15px;cursor:pointer;margin-right:8px}
.btn.gray{background:#2a2f3a}.banner{background:rgba(248,81,73,.12);border:1px solid var(--bad);color:#ffb4ae;padding:12px;border-radius:10px;margin-bottom:18px}
.flash{background:rgba(79,140,255,.12);border:1px solid var(--accent);padding:10px;border-radius:10px;margin-bottom:18px}
.note{background:rgba(255,196,0,.1);border:1px solid #6b5800;color:#ffd98a;padding:12px;border-radius:10px;margin:18px 0}
a{color:var(--accent)}.foot{color:var(--muted);font-size:13px;margin-top:28px}code{background:#000;padding:1px 6px;border-radius:5px}
`

const homeTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Home server</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>🏠 Home server</h1>
<p class="sub">Your private apps. New device? Install the certificate first.</p>

<div class="note"><b>First time on this device:</b> open
<a href="/root.crt">Install the certificate</a> so the apps below load without warnings.</div>

<div class="grid">
  <a class="card" href="https://{{.Cfg.VaultHost}}"><h3>🔑 Passwords</h3>
    <p>Vaultwarden — save & sync passwords. (Bitwarden apps: server <code>https://{{.Cfg.VaultHost}}</code>)</p></a>
  <a class="card" href="https://{{.Cfg.CloudHost}}"><h3>☁️ Files</h3>
    <p>Nextcloud — files, photos, calendar.</p></a>
  <a class="card" href="https://{{.Cfg.PiholeHost}}"><h3>🛡️ Ad blocker</h3>
    <p>Pi-hole — network-wide ad blocking admin.</p></a>
  <a class="card" href="{{.VPNUI}}"><h3>🔒 VPN</h3>
    <p>Add a device for secure access from outside home.</p></a>
  <a class="card" href="/root.crt"><h3>📜 Certificate</h3>
    <p>Download &amp; install the trust certificate (do this once per device).</p></a>
</div>
<p class="foot">Trouble? See ONBOARDING.md, or ask your admin. · <a href="/admin">Admin</a></p>
</div></body></html>`

const adminTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>hsctl admin</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>⚙️ Admin</h1>
<p class="sub">Server {{.Cfg.ServerIP}} · timezone {{.Cfg.TZ}}</p>

{{if .Msg}}<div class="flash">{{.Msg}}</div>{{end}}
{{if .DockerErr}}<div class="banner">{{.DockerErr}}</div>{{end}}

<h3>Services</h3>
<table><tr><th>Container</th><th>State</th><th>Status</th></tr>
{{range .Containers}}<tr><td>{{.Name}}</td>
<td>{{if .Running}}<span class="tag ok">running</span>{{else}}<span class="tag bad">{{.State}}</span>{{end}}</td>
<td>{{.Status}}</td></tr>{{else}}<tr><td colspan="3">No stack containers found.</td></tr>{{end}}
</table>

<form method="post" action="/admin/action" style="display:inline">
  <input type="hidden" name="do" value="up"><button class="btn">Start all</button></form>
<form method="post" action="/admin/action" style="display:inline">
  <input type="hidden" name="do" value="restart"><button class="btn gray">Restart</button></form>
<form method="post" action="/admin/action" style="display:inline"
  onsubmit="return confirm('Stop all services?')">
  <input type="hidden" name="do" value="down"><button class="btn gray">Stop all</button></form>

<div class="note">Backups are configured on the command line for now (<code>hsctl backup</code>) — a
panel here is coming next.</div>
<p class="foot"><a href="/">← Home portal</a></p>
</div></body></html>`
