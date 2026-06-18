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
.btn.gray{background:#2a2f3a}.btn.red{background:var(--bad)}.banner{background:rgba(248,81,73,.12);border:1px solid var(--bad);color:#ffb4ae;padding:12px;border-radius:10px;margin-bottom:18px}
.flash{background:rgba(79,140,255,.12);border:1px solid var(--accent);padding:10px;border-radius:10px;margin-bottom:18px}
.note{background:rgba(255,196,0,.1);border:1px solid #6b5800;color:#ffd98a;padding:12px;border-radius:10px;margin:18px 0}
a{color:var(--accent)}.foot{color:var(--muted);font-size:13px;margin-top:28px}code{background:#000;padding:1px 6px;border-radius:5px}
.md{font-size:15.5px;line-height:1.65}
.md h1{font-size:24px;margin:6px 0 14px;border-bottom:1px solid #262b34;padding-bottom:8px}
.md h2{font-size:19px;margin:28px 0 8px}.md h3{font-size:16px;margin:20px 0 6px}
.md p{margin:10px 0}.md img{max-width:100%}
.md ul,.md ol{padding-left:22px;margin:8px 0}.md li{margin:5px 0}
.md table{width:100%;border-collapse:collapse;margin:14px 0;font-size:14.5px}
.md th,.md td{border:1px solid #2a2f3a;padding:7px 11px;text-align:left}.md th{background:#1a1d24}
.md code{background:#000;padding:1px 6px;border-radius:5px;font-size:.92em}
.md pre{background:#0b0d11;border:1px solid #262b34;border-radius:8px;padding:12px;overflow:auto}
.md pre code{background:none;padding:0}
.md blockquote{border-left:3px solid var(--accent);margin:14px 0;padding:6px 14px;color:var(--muted);background:rgba(79,140,255,.06)}
.md hr{border:0;border-top:1px solid #262b34;margin:24px 0}
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
  <a class="card" href="/help"><h3>📖 Setup guide</h3>
    <p>First-time setup &amp; per-device help — cert, accounts, the apps.</p></a>
  <a class="card" href="/admin/backup"><h3>💾 Backup &amp; restore</h3>
    <p>Back the server up, or restore it from a backup (admin login).</p></a>
</div>
<p class="foot">Trouble? Open the <a href="/help">Setup guide</a>, or ask your admin. · <a href="/admin">Admin</a></p>
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

<p style="margin-top:18px">
<form method="post" action="/admin/action" style="display:inline"
  onsubmit="return confirm('Shut down the whole server?\n\nIt will power off and someone will need to switch it back on by hand. The apps come back automatically once it boots.')">
  <input type="hidden" name="do" value="shutdown"><button class="btn red">⏻ Shut down server</button></form>
</p>

<p style="margin-top:18px"><a href="/admin/backup">💾 Backups →</a></p>
<p class="foot"><a href="/">← Home portal</a> · <a href="/logout">Log out</a></p>
</div></body></html>`

const loginTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Admin login</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>🔒 Admin login</h1>
<p class="sub">Sign in to manage the home server.</p>
{{if .Err}}<div class="banner">{{.Err}}</div>{{end}}
<form method="post" action="/login" style="max-width:360px">
  <input type="hidden" name="next" value="{{.Next}}">
  <p><label>Username<br>
  <input name="username" autocomplete="username" value="admin"
   style="width:100%;padding:9px;background:#0b0d11;color:#e7e9ee;border:1px solid #2a2f3a;border-radius:8px"></label></p>
  <p><label>Password<br>
  <input type="password" name="password" autocomplete="current-password" autofocus
   style="width:100%;padding:9px;background:#0b0d11;color:#e7e9ee;border:1px solid #2a2f3a;border-radius:8px"></label></p>
  <button class="btn">Sign in</button>
</form>
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

<h3 style="margin-top:24px">Restore</h3>
<p>Recover the whole server from a snapshot (stops the stack, puts every volume back, starts it again).</p>
<p><a href="/admin/backup/restore">♻️ Restore from a backup →</a></p>

<p class="foot"><a href="/admin">← Admin</a></p>
</div></body></html>`

const restoreTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Restore</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>♻️ Restore from backup</h1>
<p class="sub">Disaster recovery — put a snapshot's data back into the stack.</p>
{{if .Msg}}<div class="flash">{{.Msg}}</div>{{end}}
<div class="banner"><b>This is destructive.</b> It STOPS all services, WIPES every data volume,
and replaces it with the snapshot's contents (Vaultwarden from its staged copy), then starts
everything again. Anything not in the snapshot is lost. The apps are offline while it runs
(can be several minutes for large data). <b>Tip:</b> run this from the server's direct address
(<code>http://SERVER:PORT/admin</code>), not the https one — the restore restarts the web proxy,
so over https this page may drop (the restore still completes).</div>

<h3>Available snapshots</h3>
<pre style="background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:12px;overflow:auto">{{if .Snapshots}}{{.Snapshots}}{{else}}(no snapshots / restic not available){{end}}</pre>

<form method="post" action="/admin/backup/restore"
  onsubmit="return confirm('Really restore? All services will stop and their data will be overwritten from the backup.')">
  <p>Snapshot to restore (blank = latest):<br>
  <input name="snapshot" placeholder="latest" autocomplete="off"
   style="width:100%;padding:8px;background:#0b0d11;color:#e7e9ee;border:1px solid #2a2f3a;border-radius:8px"></p>
  <p>Type <b>RESTORE</b> to confirm:<br>
  <input name="confirm" autocomplete="off"
   style="width:100%;padding:8px;background:#0b0d11;color:#e7e9ee;border:1px solid #2a2f3a;border-radius:8px"></p>
  <button class="btn red">♻️ Restore now</button>
</form>
<p class="foot"><a href="/admin/backup">← Cancel</a></p>
</div></body></html>`

const helpTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Setup guide</title>
<style>{{css}}</style></head><body><div class="wrap">
<p><a href="/">← Dashboard</a></p>
<div class="md">{{.Body}}</div>
<p class="foot"><a href="/">← Back to dashboard</a></p>
</div></body></html>`
