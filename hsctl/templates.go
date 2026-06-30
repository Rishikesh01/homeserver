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
.btn.green{background:var(--ok)}.btn:disabled{opacity:.5;cursor:not-allowed}
.cmd{background:var(--card);border:1px solid #262b34;border-radius:12px;padding:16px;margin:0 0 12px}
.cmd h4{margin:0 0 4px;font-size:16px}.cmd p{margin:0 0 12px;color:var(--muted);font-size:14px}
.badge{display:inline-block;font-size:11px;font-weight:600;padding:1px 7px;border-radius:99px;margin-left:8px;vertical-align:middle}
.badge.caution{background:rgba(255,196,0,.15);color:#ffd98a}.badge.destructive{background:rgba(248,81,73,.15);color:var(--bad)}
.badge.root{background:rgba(154,163,178,.15);color:var(--muted)}.badge.slow{background:rgba(79,140,255,.15);color:var(--accent)}
.out{background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:12px;overflow:auto;white-space:pre-wrap;word-break:break-word;min-height:60px;max-height:55vh;font:13px/1.45 ui-monospace,Menlo,Consolas,monospace}
.in{width:100%;padding:8px;background:#0b0d11;color:var(--fg);border:1px solid #2a2f3a;border-radius:8px}
.kv{display:grid;grid-template-columns:max-content 1fr;gap:6px 16px;margin:10px 0 18px;font-size:14.5px}
.kv .k{color:var(--muted)}.tools{display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:12px;margin:8px 0 22px}
.tool{background:var(--card);border:1px solid #262b34;border-radius:12px;padding:16px;text-decoration:none;color:inherit;text-align:center}
.tool:hover{border-color:var(--accent)}.tool .ico{font-size:24px}.tool .t{margin-top:6px;font-weight:600}
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

<div class="tools">
  <a class="tool" href="/admin/commands"><div class="ico">🧰</div><div class="t">Commands</div></a>
  <a class="tool" href="/admin/devices"><div class="ico">💽</div><div class="t">Drives</div></a>
  <a class="tool" href="/admin/backup"><div class="ico">💾</div><div class="t">Backups</div></a>
  <a class="tool" href="/admin/terminal"><div class="ico">⌨️</div><div class="t">Terminal</div></a>
</div>

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
<p class="sub">Encrypted, deduplicated snapshots via restic — your passwords, files, and settings.</p>
{{if .Msg}}<div class="flash">{{.Msg}}</div>{{end}}
{{if not .ResticOK}}<div class="banner">restic isn't installed on the server yet.
Install it: <code>sudo apt-get install -y restic</code>, then reload this page.</div>{{end}}

<h3>Status</h3>
<div class="kv">
  <div class="k">restic</div><div>{{if .ResticOK}}installed{{if .ResticVersion}} · v{{.ResticVersion}}{{end}}{{else}}<span style="color:var(--bad)">not installed</span>{{end}}</div>
  <div class="k">Destination</div><div><code>{{.Repo}}</code></div>
  <div class="k">Retention</div><div>{{.Retention}}</div>
  <div class="k">Disk guard</div><div>{{if .GuardPath}}<code>{{.GuardPath}}</code> — {{if .GuardOK}}<span class="tag ok">mounted</span>{{else}}<span class="tag bad">NOT mounted</span> (backups will refuse to run until you mount it on the <a href="/admin/devices">Drives</a> page){{end}}{{else}}none (backups go to the path above as-is){{end}}</div>
  {{if .Stats}}<div class="k">Repo size</div><div><span class="foot">{{.Stats}}</span></div>{{end}}
</div>

<h3>Destination</h3>
<form method="post" action="/admin/backup/config">
  <p>Where to store backups (off-box, or an external disk, strongly recommended):<br>
  <input class="in" name="repo" value="{{.Repo}}"></p>
  <p class="foot">Examples — external disk: <code>/mnt/backup/restic</code> · another host:
  <code>sftp:user@host:/backups</code> · Backblaze B2: <code>b2:bucket:homeserver</code></p>
  <p>Retention (how many to keep): <input class="in" name="retention" value="{{.Retention}}"></p>
  <button class="btn gray">Save destination</button>
</form>

<h3 style="margin-top:24px">Operations</h3>
<p class="foot">Output appears below each runs. Reading Docker volumes needs root — these run with the dashboard's privileges.</p>
<button class="btn green" data-slug="backup-run" data-confirm="" data-reload="1">Back up now</button>
<button class="btn gray" data-slug="backup-init" data-confirm="" data-reload="1">Initialize repo</button>
<button class="btn gray" data-slug="backup-list" data-confirm="">List snapshots</button>
<button class="btn gray" data-slug="backup-forget" data-confirm="Prune old snapshots beyond the retention policy? Pruned snapshots are gone for good." data-reload="1">Prune old</button>
<button class="btn green" data-slug="backup-verify" data-confirm="">Self-test</button>
<div id="out" class="out" style="margin-top:12px">Snapshots and command output appear here.</div>

<h3 style="margin-top:24px">Snapshots</h3>
<pre style="background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:12px;overflow:auto">{{if .Snapshots}}{{.Snapshots}}{{else}}(no snapshots yet — set a destination, Initialize, then Back up now){{end}}</pre>

<div class="note">First time: set a destination above, then click <b>Initialize repo</b>, then <b>Back up now</b>.
Keep the repo password file (<code>.restic-password</code>) safe — without it, backups can't be decrypted.</div>

<h3 style="margin-top:24px">Restore</h3>
<p>Recover the whole server from a snapshot (stops the stack, puts every volume back, starts it again).</p>
<p><a href="/admin/backup/restore">♻️ Restore from a backup →</a></p>

<p class="foot"><a href="/admin">← Admin</a></p>
<script>` + runJS + `</script>
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

// runJS is the shared client that POSTs a command slug to /admin/run and streams the
// combined output into the page's #out pane live. Shared by the Command Center and the
// Backups page. It contains no template actions, so it passes through html/template as
// literal script text. (Careful editing: avoid literal "{{" or "}}" — they're template
// delimiters; build any nested object so its closing braces aren't adjacent.)
const runJS = `
async function runCmd(slug, confirmMsg, reload){
  if(confirmMsg && !window.confirm(confirmMsg)) return;
  const out=document.getElementById('out');
  out.textContent='Running…\n';
  document.querySelectorAll('button[data-slug]').forEach(b=>b.disabled=true);
  var ok=false;
  try{
    var body='slug='+encodeURIComponent(slug);
    if(confirmMsg) body+='&confirm='+encodeURIComponent(slug); // server-side guard for destructive ops
    const res=await fetch('/admin/run',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:body});
    if(!res.ok){ out.textContent='error '+res.status+': '+(await res.text()); return; }
    out.textContent='';
    const reader=res.body.getReader(), dec=new TextDecoder();
    for(;;){
      const step=await reader.read();
      if(step.done) break;
      out.textContent+=dec.decode(step.value,{stream:true});
      out.scrollTop=out.scrollHeight;
    }
    ok=out.textContent.trimEnd().endsWith('[done]');
  }catch(e){ out.textContent+='\nrequest failed: '+e; }
  finally{ document.querySelectorAll('button[data-slug]').forEach(b=>b.disabled=false); }
  if(ok && reload){ setTimeout(function(){ location.reload(); }, 1200); } // refresh server-rendered panels (e.g. Snapshots)
}
document.querySelectorAll('button[data-slug]').forEach(function(b){
  b.addEventListener('click',function(){ runCmd(b.dataset.slug, b.dataset.confirm, b.dataset.reload); });
});`

const commandsTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Commands</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>🧰 Command Center</h1>
<p class="sub">Everything the server's control tool can do — explained. Click <b>Run</b> and watch the output below.</p>

{{range .Groups}}<h3>{{.Name}}</h3>
{{range .Cmds}}<div class="cmd">
  <h4>{{.Title}}{{if .IsDestructive}}<span class="badge destructive">destructive</span>{{else if .IsCaution}}<span class="badge caution">changes things</span>{{end}}{{if .NeedsRoot}}<span class="badge root">needs root</span>{{end}}{{if .Slow}}<span class="badge slow">may take a while</span>{{end}}</h4>
  <p>{{.Desc}}</p>
  <button class="btn {{.BtnClass}}" data-slug="{{.Slug}}" data-confirm="{{.Confirm}}">Run</button>
</div>
{{end}}{{end}}

<h3>Output</h3>
<div id="out" class="out">Pick a command above and click Run — its output appears here as it runs.</div>
<p class="foot"><a href="/admin">← Admin</a> · <a href="/admin/terminal">Open a full terminal →</a></p>
<script>` + runJS + `</script>
</div></body></html>`

const devicesTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Drives</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>💽 Drives</h1>
<p class="sub">Storage attached to the server. Mount one to start using it (for example, as a backup destination).</p>
{{if .Msg}}<div class="flash">{{.Msg}}</div>{{end}}
{{if .Err}}<div class="banner">{{.Err}}</div>{{end}}
{{if and .GuardPath (not .GuardOK)}}<div class="note">Your backups require a disk mounted at <code>{{.GuardPath}}</code>, which isn't mounted right now — use the green <b>Mount for backups</b> button next to the right drive so the <a href="/admin/backup">Backups</a> page will run.</div>{{end}}

<table>
<tr><th>Device</th><th>Size</th><th>Format</th><th>Label</th><th>Mounted at</th><th></th></tr>
{{range .Rows}}<tr>
  <td><code>{{.Path}}</code><br><span class="foot">{{.Parent}}{{if .Removable}} · removable{{end}}{{if .ReadOnly}} · read-only{{end}}</span></td>
  <td>{{.Size}}</td>
  <td>{{if .FSType}}{{.FSType}}{{else}}—{{end}}</td>
  <td>{{if .Label}}{{.Label}}{{else}}—{{end}}</td>
  <td>{{if .Mountpoint}}<span class="tag ok">{{.Mountpoint}}</span>{{else}}<span class="foot">not mounted</span>{{end}}</td>
  <td>{{if .Mountpoint}}<form method="post" action="/admin/devices/unmount" style="margin:0" onsubmit="return confirm('Eject {{.Path}}? Make sure nothing is reading or writing it.')"><input type="hidden" name="dev" value="{{.Path}}"><button class="btn gray">Eject</button></form>{{else if .Mountable}}<form method="post" action="/admin/devices/mount" style="margin:0 0 4px"><input type="hidden" name="dev" value="{{.Path}}"><button class="btn">Mount</button></form>{{if and $.GuardPath (not $.GuardOK)}}<form method="post" action="/admin/devices/mount" style="margin:0" onsubmit="return confirm('Mount {{.Path}} at {{$.GuardPath}} for backups?')"><input type="hidden" name="dev" value="{{.Path}}"><input type="hidden" name="target" value="{{$.GuardPath}}"><button class="btn green">Mount for backups</button></form>{{end}}{{else}}<span class="foot">{{.Why}}</span>{{end}}</td>
</tr>{{else}}<tr><td colspan="6">No drives detected.</td></tr>{{end}}
</table>

<div class="note">Mounts land under <code>{{.MountAt}}/&lt;label&gt;</code> and are <b>temporary</b> — they clear on reboot, and no permanent (fstab) entry is written. That's deliberate: it matches the by-hand way the backup disk is attached. After mounting a disk for backups, set it as the destination on the <a href="/admin/backup">Backups</a> page (e.g. <code>{{.MountAt}}/backup/restic</code>).</div>
<p class="foot"><a href="/admin">← Admin</a></p>
</div></body></html>`

const terminalTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Terminal</title>
<link rel="stylesheet" href="/admin/assets/xterm.css">
<style>{{css}}
#term{height:76vh;padding:8px;background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px}</style>
</head><body><div class="wrap">
<h1>⌨️ Terminal</h1>
<p class="sub">A real shell on the server, running as the dashboard's user.</p>
<div class="banner"><b>This is a full shell (root under the service).</b> Anything you type runs for real. Only the admin login can reach it, and it isn't exposed outside your network — but treat it with the same care as sitting at the machine.</div>
<div id="term"></div>
<p class="foot"><a href="/admin">← Admin</a> · blank screen? Click it and press Enter.</p>
<script src="/admin/assets/xterm.js"></script>
<script src="/admin/assets/addon-fit.js"></script>
<script>
var term=new Terminal({cursorBlink:true,fontFamily:'ui-monospace,Menlo,Consolas,monospace',fontSize:14,theme:{background:'#0b0d11',foreground:'#e7e9ee'}});
var fit=new FitAddon.FitAddon(); term.loadAddon(fit);
term.open(document.getElementById('term')); fit.fit();
var proto=location.protocol==='https:'?'wss':'ws';
var ws=new WebSocket(proto+'://'+location.host+'/admin/terminal/ws'); ws.binaryType='arraybuffer';
var enc=new TextEncoder();
ws.onmessage=function(e){ term.write(typeof e.data==='string'?e.data:new Uint8Array(e.data)); };
function sendResize(){ if(ws.readyState===1){ fit.fit(); var inner={cols:term.cols,rows:term.rows}; ws.send(JSON.stringify({resize:inner})); } }
ws.onopen=function(){ sendResize(); term.focus(); };
ws.onclose=function(){ term.write('\r\n\r\n[disconnected — reload the page to reconnect]\r\n'); };
ws.onerror=function(){ term.write('\r\n[connection error]\r\n'); };
term.onData(function(d){ if(ws.readyState===1) ws.send(enc.encode(d)); });
addEventListener('resize',sendResize);
</script>
</div></body></html>`
