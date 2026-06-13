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
  {{range .Services}}<a class="card" href="{{.URL}}"><h3>{{.Icon}} {{.Name}}</h3>
    <p>{{.Desc}}</p></a>
  {{end}}<a class="card" href="/root.crt"><h3>📜 Certificate</h3>
    <p>Install this once per device so the apps load without warnings.</p></a>
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

<p style="margin-top:18px"><a href="/admin/backup">💾 Backups →</a></p>
<p class="foot"><a href="/">← Home portal</a></p>
</div></body></html>`

const backupTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Backups</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>💾 Backups</h1>
<p class="sub">Encrypted, deduplicated snapshots via restic.</p>
{{if .Msg}}<div class="flash">{{.Msg}}</div>{{end}}
{{if not .ResticOK}}<div class="banner">restic isn't installed on the server yet.
Install it: <code>sudo apt-get install -y restic</code>, then reload this page.</div>{{end}}

<h3>Destination</h3>
<form method="post" action="/admin/backup/config">
  <p>Where to store backups (off-box strongly recommended):<br>
  <input name="repo" value="{{.Repo}}" style="width:100%;padding:8px;background:#0b0d11;color:#e7e9ee;border:1px solid #2a2f3a;border-radius:8px">
  </p>
  <p class="foot">Examples — USB: <code>/mnt/usb/restic</code> · another host:
  <code>sftp:user@host:/backups</code> · Backblaze: <code>b2:bucket:homeserver</code></p>
  <p>Retention: <input name="retention" value="{{.Retention}}" style="width:100%;padding:8px;background:#0b0d11;color:#e7e9ee;border:1px solid #2a2f3a;border-radius:8px"></p>
  <button class="btn gray">Save destination</button>
</form>

<h3 style="margin-top:24px">Snapshots</h3>
<form method="post" action="/admin/backup/run" style="display:inline">
  <button class="btn">Back up now</button></form>
<pre style="background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:12px;overflow:auto;margin-top:12px">{{if .Snapshots}}{{.Snapshots}}{{else}}(restic not available){{end}}</pre>

<div class="note">Backing up reads Docker volume files, which need root — so the
scheduled backups run from a root timer, and a UI/CLI run needs the tool to have that
access. First time: set a destination above, then run <code>sudo hsctl backup init</code>.</div>
<p class="foot"><a href="/admin">← Admin</a></p>
</div></body></html>`
