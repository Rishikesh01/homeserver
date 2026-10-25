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
.flash.err{background:rgba(248,81,73,.12);border-color:var(--bad);color:#ffb4ae}
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
.meter{background:#0b0d11;border:1px solid #2a2f3a;border-radius:99px;height:10px;overflow:hidden;margin-top:6px}
.meter>i{display:block;height:100%;background:var(--ok)}.meter>i.warn{background:#d9a400}.meter>i.hot{background:var(--bad)}
.stat{background:var(--card);border:1px solid #262b34;border-radius:12px;padding:14px 16px}
.stat .k{color:var(--muted);font-size:13px}.stat .v{font-size:20px;font-weight:600;margin-top:2px}.stat .d{color:var(--muted);font-size:13px;margin-top:4px}
.stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin:8px 0 22px}
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

{{with .Flash}}{{if .Msg}}<div class="flash{{if .Err}} err{{end}}" role="status">{{.Msg}}</div>{{end}}{{end}}
{{if .Busy}}<div class="note" role="status">⏳ Running now: {{.Busy}}</div>{{end}}
{{if not .SetupDone}}<div class="note"><b>Setup isn't finished</b> — the apps aren't configured yet. <a href="/setup">Continue the setup wizard →</a></div>{{end}}
{{if .DockerErr}}<div class="banner">{{.DockerErr}}</div>{{end}}

<div class="tools">
  <a class="tool" href="/admin/apps"><div class="ico">🧩</div><div class="t">Apps</div></a>
  <a class="tool" href="/admin/commands"><div class="ico">🧰</div><div class="t">Commands</div></a>
  <a class="tool" href="/admin/updates"><div class="ico">⬆️</div><div class="t">Updates</div></a>
  <a class="tool" href="/admin/devices"><div class="ico">💽</div><div class="t">Drives</div></a>
  <a class="tool" href="/admin/backup"><div class="ico">💾</div><div class="t">Backups</div></a>
  <a class="tool" href="/admin/terminal"><div class="ico">⌨️</div><div class="t">Terminal</div></a>
</div>

<h3>System</h3>
<div class="stats">
  {{if .Sys.CPUOK}}<div class="stat"><div class="k">CPU</div><div class="v">{{.Sys.CPUPct}}% busy</div>
    <div class="meter"><i class="{{meterClass .Sys.CPUPct}}" style="width:{{.Sys.CPUPct}}%"></i></div>
    <div class="d">{{.Sys.Cores}} cores{{if .Sys.Load1}} · load {{.Sys.Load1}}{{end}}</div></div>{{end}}
  {{if .Sys.MemOK}}<div class="stat"><div class="k">Memory</div><div class="v">{{.Sys.MemPct}}% used</div>
    <div class="meter"><i class="{{meterClass .Sys.MemPct}}" style="width:{{.Sys.MemPct}}%"></i></div>
    <div class="d">{{.Sys.MemUsed}} of {{.Sys.MemTotal}}</div></div>{{end}}
  {{if .Sys.RootDisk.OK}}<div class="stat"><div class="k">Disk space · system</div><div class="v">{{.Sys.RootDisk.Pct}}% used</div>
    <div class="meter"><i class="{{meterClass .Sys.RootDisk.Pct}}" style="width:{{.Sys.RootDisk.Pct}}%"></i></div>
    <div class="d">{{.Sys.RootDisk.Used}} of {{.Sys.RootDisk.Total}}</div></div>{{end}}
  {{if .Sys.BackupDisk.OK}}<div class="stat"><div class="k">Disk space · backup disk</div><div class="v">{{.Sys.BackupDisk.Pct}}% used</div>
    <div class="meter"><i class="{{meterClass .Sys.BackupDisk.Pct}}" style="width:{{.Sys.BackupDisk.Pct}}%"></i></div>
    <div class="d">{{.Sys.BackupDisk.Used}} of {{.Sys.BackupDisk.Total}}</div></div>{{else if .Sys.BackupGuard}}<div class="stat"><div class="k">Backup disk</div><div class="v"><span style="color:var(--bad)">✖ not mounted</span></div>
    <div class="d">Nothing at <code>{{.Sys.BackupGuard}}</code> — mount it on the <a href="/admin/devices">Drives</a> page, or backups will fail</div></div>{{end}}
  <div class="stat"><div class="k">Last backup</div>
    <div class="v">{{if .Backup.Failed}}<span style="color:var(--bad)">✖ failed {{.Backup.FailedAge}}</span>{{else if not .Backup.Known}}<span style="color:var(--bad)">⚠ never</span>{{else if .Backup.Stale}}<span style="color:var(--bad)">⚠ {{.Backup.Age}}</span>{{else}}<span style="color:var(--ok)">✔ {{.Backup.Age}}</span>{{end}}</div>
    <div class="d">{{if .Backup.Failed}}{{.Backup.FailedErr}}{{if .Backup.Known}} · last good one {{.Backup.Age}}{{end}} — see the <a href="/admin/backup">Backups</a> page{{else if .Backup.Stale}}Overdue — run one on the <a href="/admin/backup">Backups</a> page{{else if not .Backup.Known}}No backup yet — set one up on the <a href="/admin/backup">Backups</a> page{{else}}via the <a href="/admin/backup">Backups</a> page{{end}}</div></div>
  {{range .Sys.Disks}}<div class="stat"><div class="k">Disk health · <code>{{.Path}}</code></div>
    <div class="v">{{if .OK}}<span style="color:var(--ok)">✔ healthy</span>{{else if eq .Status "FAILING"}}<span style="color:var(--bad)">✖ FAILING — back up now</span>{{else}}<span style="color:var(--muted)">unknown</span>{{end}}</div>
    <div class="d">{{if .Model}}{{.Model}} · {{end}}{{.Size}}</div></div>{{end}}
</div>
{{if .Sys.SmartMsg}}<div class="note">{{.Sys.SmartMsg}}</div>{{end}}
{{if .Sys.DisksErr}}<div class="banner">Couldn't check the disks: {{.Sys.DisksErr}}</div>{{end}}

<h3>Services</h3>
<table><tr><th>Container</th><th>State</th><th>Status</th></tr>
{{range .Containers}}<tr><td>{{.Name}}</td>
<td>{{if .Running}}<span class="tag ok">running</span>{{else}}<span class="tag bad">{{.State}}</span>{{end}}</td>
<td>{{.Status}}</td></tr>{{else}}<tr><td colspan="3">No stack containers found.</td></tr>{{end}}
</table>

<button class="btn" data-slug="up" data-confirm="" data-reload="1">Start all</button>
<button class="btn gray" data-slug="restart" data-confirm="Restart all apps? They go offline for a minute or so. This dashboard stays up." data-reload="1">Restart apps</button>
<button class="btn gray" data-slug="down" data-confirm="Stop all apps? They stay offline (even after a reboot) until you start them again here. This dashboard stays up." data-reload="1">Stop apps</button>
<p id="jobstatus" class="foot" aria-live="polite"></p>
<div id="out" class="out" data-job="{{.JobID}}" data-reload="1" hidden></div>

<p style="margin-top:18px">
<form method="post" action="/admin/action" style="display:inline"
  onsubmit="return confirm('Shut down the whole server?\n\nIt will power off and someone will need to switch it back on by hand. The apps come back automatically once it boots.')">
  <input type="hidden" name="do" value="shutdown"><button class="btn red">⏻ Shut down server</button></form>
</p>

<p class="foot"><a href="/">← Home portal</a> · <a href="/logout">Log out</a></p>
<script>` + runJS + `</script>
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
  <input class="in" name="username" autocomplete="username" value="admin"></label></p>
  <p><label>Password<br>
  <input class="in" type="password" name="password" autocomplete="current-password" autofocus></label></p>
  <button class="btn">Sign in</button>
</form>
<p class="foot"><a href="/">← Home portal</a></p>
</div></body></html>`

const backupTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Backups</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>💾 Backups</h1>
<p class="sub">Encrypted, deduplicated snapshots via restic — your passwords, files, and settings.</p>
{{with .Flash}}{{if .Msg}}<div class="flash{{if .Err}} err{{end}}" role="status">{{.Msg}}</div>{{end}}{{end}}
{{if not .ResticOK}}<div class="banner">restic isn't installed on the server yet.
Install it: <code>sudo apt-get install -y restic</code>, then reload this page.</div>{{end}}

<h3>Status</h3>
<div class="kv">
  <div class="k">restic</div><div>{{if .ResticOK}}installed{{if .ResticVersion}} · v{{.ResticVersion}}{{end}}{{else}}<span style="color:var(--bad)">not installed</span>{{end}}</div>
  <div class="k">Last backup</div><div>{{if .Last.Known}}{{if .Last.Stale}}<span style="color:var(--bad)">⚠ {{.Last.Age}} — overdue</span>{{else}}<span style="color:var(--ok)">✔ {{.Last.Age}}</span>{{end}}{{else}}<span style="color:var(--bad)">⚠ never</span>{{end}}</div>
  {{if .Last.Failed}}<div class="k">Last attempt</div><div><span style="color:var(--bad)">✖ failed {{.Last.FailedAge}}:</span> {{.Last.FailedErr}}</div>{{end}}
  <div class="k">Nightly backups</div><div>{{if .Timer.Enabled}}<span class="tag ok">on</span>{{if .Timer.Next}} · next run {{.Timer.Next}}{{end}}{{else if .Timer.Installed}}<span class="tag bad">off</span> — enable with <code>sudo systemctl enable --now hsctl-backup.timer</code>{{else}}<span class="tag bad">not set up</span> — install with <code>sudo hsctl install</code>{{end}}</div>
  <div class="k">Failure alerts</div><div>{{if .Notify}}<code>{{.Notify}}</code>{{else}}none — add a notification URL below to hear about a failed backup{{end}}</div>
  <div class="k">Destination</div><div><code>{{.Repo}}</code>{{if .SystemDisk}}<br><span style="color:var(--bad)">⚠ This is on the same disk as {{.SystemDisk}} — that protects against mistakes, not against the disk dying. Point it at an external disk or another machine.</span>{{end}}{{if .GuardWarn}}<br><span style="color:var(--bad)">⚠ {{.GuardWarn}}</span>{{end}}</div>
  <div class="k">Retention</div><div>{{.Retention}}{{if .RetentionErr}}<br><span style="color:var(--bad)">⚠ {{.RetentionErr}} — nothing is pruned until it's fixed below.</span>{{end}}</div>
  <div class="k">Disk guard</div><div>{{if .GuardPath}}<code>{{.GuardPath}}</code> — {{if .GuardOK}}<span class="tag ok">mounted</span>{{else}}<span class="tag bad">NOT mounted</span> (backups will refuse to run until you mount it on the <a href="/admin/devices">Drives</a> page){{end}}{{else}}none (backups go to the path above as-is){{end}}</div>
  <div class="k">Off-site replica</div><div>{{if .Replica}}<code>{{.Replica}}</code> — {{if .ReplicaAge}}last copied {{.ReplicaAge}}{{else}}<span style="color:var(--bad)">never copied yet</span>{{end}} (copied after every backup){{else}}none — set one below for fire/theft-proof backups{{end}}</div>
  {{if .Stats}}<div class="k">Repo size</div><div><span class="foot">{{.Stats}}</span></div>{{end}}
</div>

<h3>Destination</h3>
<form method="post" action="/admin/backup/config">
  <p>Where to store backups (off-box, or an external disk, strongly recommended):<br>
  <input class="in" name="repo" value="{{.Repo}}"></p>
  <p class="foot">Examples — external disk: <code>/mnt/backup/restic</code> · another host:
  <code>sftp:user@host:/backups</code> · S3: <code>s3:s3.region.amazonaws.com/bucket</code></p>
  <p>Disk guard — backups refuse to run unless a disk is mounted here (set automatically for <code>/mnt/…</code> and <code>/media/…</code> destinations; empty = off):<br>
  <input class="in" name="require_mount" value="{{.GuardPath}}" placeholder="/mnt/backup"></p>
  <fieldset style="border:1px solid #2a2f3a;border-radius:8px;padding:8px 12px;margin:12px 0">
  <legend>How many snapshots to keep</legend>
  <label>Daily <input class="in" name="keep_daily" type="number" min="0" value="{{.KeepDaily}}" style="width:6em"></label>
  <label>Weekly <input class="in" name="keep_weekly" type="number" min="0" value="{{.KeepWeekly}}" style="width:6em"></label>
  <label>Monthly <input class="in" name="keep_monthly" type="number" min="0" value="{{.KeepMonthly}}" style="width:6em"></label>
  <input type="hidden" name="keep_rest" value="{{.KeepRest}}">
  <p class="foot" style="margin-top:6px">Older snapshots are pruned after each backup. Leave a box empty (or 0) to not keep any of that kind.{{if .KeepRest}} Also kept, from your config: <code>{{.KeepRest}}</code>.{{end}}</p>
  </fieldset>
  <p>Off-site replica (optional second copy, e.g. Backblaze B2 or another host):<br>
  <input class="in" name="replica" value="{{.Replica}}" placeholder="sftp:user@nas:/backups"></p>
  <p class="foot">Cloud replicas need credentials in <code>.backup-env</code> next to the repo
  (e.g. <code>AWS_ACCESS_KEY_ID=…</code> and <code>AWS_SECRET_ACCESS_KEY=…</code>, one per line). SFTP and local paths don't.
  Every backup then copies itself there (the first one creates the replica repo); <b>Copy off-site</b> below does it now.</p>
  <p>Tell me when a backup fails (optional — a URL that gets a message, e.g. an <a href="https://ntfy.sh">ntfy</a> topic):<br>
  <input class="in" name="notify" value="{{.Notify}}" placeholder="https://ntfy.sh/your-secret-topic"></p>
  <p class="foot">Clearing the replica or notification box and saving turns it off.</p>
  <button class="btn gray">Save</button>
</form>

<h3 style="margin-top:24px">Operations</h3>
<p class="foot">Output appears below as each one runs, and it keeps running if you leave this page. Reading Docker volumes needs root — these run with the dashboard's privileges.</p>
{{if .Busy}}<div class="note" role="status">⏳ Running now: {{.Busy}}</div>{{end}}
<button class="btn green" data-slug="backup-run" data-confirm="" data-reload="1">Back up now</button>
<button class="btn gray" data-slug="backup-init" data-confirm="" data-reload="1">Initialize repo</button>
<button class="btn gray" data-slug="backup-list" data-confirm="">List snapshots</button>
<button class="btn gray" data-slug="backup-forget" data-confirm="Prune old snapshots beyond the retention policy? Pruned snapshots are gone for good." data-reload="1">Prune old</button>
<button class="btn green" data-slug="backup-verify" data-confirm="">Self-test</button>
<button class="btn gray" data-slug="backup-check" data-confirm="">Check backups</button>
<button class="btn gray" data-slug="backup-replicate" data-confirm="">Copy off-site</button>
<p id="jobstatus" class="foot" aria-live="polite"></p>
<div id="out" class="out" data-job="{{.JobID}}">Command output appears here.</div>

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
{{with .Flash}}{{if .Msg}}<div class="flash{{if .Err}} err{{end}}" role="status">{{.Msg}}</div>{{end}}{{end}}
<div class="banner"><b>This is destructive.</b> It STOPS all services, WIPES every data volume,
and replaces it with the snapshot's contents (Vaultwarden from its staged copy), then starts
everything again. Anything not in the snapshot is lost. The apps — including this dashboard —
are offline for a few minutes while it runs; the next page tracks progress and updates itself
when it's done, so you don't need to reload or do anything.</div>

<h3>Available snapshots</h3>
<pre style="background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:12px;overflow:auto">{{if .Snapshots}}{{.Snapshots}}{{else}}(no snapshots / restic not available){{end}}</pre>

<form method="post" action="/admin/backup/restore"
  onsubmit="return confirm('Really restore? All services will stop and their data will be overwritten from the backup.')">
  <p>Snapshot to restore (blank = latest):<br>
  <input class="in" name="snapshot" placeholder="latest" autocomplete="off"></p>
  <p>Type <b>RESTORE</b> to confirm:<br>
  <input class="in" name="confirm" autocomplete="off"></p>
  <button class="btn red">♻️ Restore now</button>
</form>
<p class="foot"><a href="/admin/backup">← Cancel</a></p>
</div></body></html>`

// restoreProgressTmpl follows the restore job (jobs.go). It keeps polling through the window
// where Caddy (the proxy) is down mid-restore, shows the job's output as it lands, and ends with
// the outcome — including exactly what failed, if anything did.
const restoreProgressTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Restoring…</title>
<style>{{css}}
.spin{display:inline-block;width:15px;height:15px;border:3px solid #2a2f3a;border-top-color:var(--accent);border-radius:50%;animation:sp 1s linear infinite;vertical-align:-2px;margin-right:9px}
@keyframes sp{to{transform:rotate(360deg)}}</style></head><body><div class="wrap">
<h1>♻️ Restoring…</h1>
<p class="sub">Putting your backup back into the stack.</p>
<div class="note" role="status" aria-live="polite"><span class="spin" id="spin"></span><span id="status">Restore in progress. The services — including this dashboard's proxy — go offline for a few minutes while it runs; <b>that's expected</b>. The restore runs on the server whether or not this page is open; it keeps checking on its own and updates when it's done.</span></div>
<p class="banner" id="hint" style="display:none">The server hasn't answered for a while. The stack is probably still restarting — keep waiting. If nothing changes after several more minutes, check on the server itself: <code>hsctl status</code>, and <code>journalctl -u hsctl-ui</code> for the dashboard's log.</p>
<div id="out" class="out" data-job="{{.JobID}}"></div>
<p class="foot" id="link" style="display:none"><a href="/admin/backup">← Back to Backups</a> · <a href="/admin">Admin</a></p>
<script>
(function(){
  var statusEl=document.getElementById('status'), spin=document.getElementById('spin'), out=document.getElementById('out'),
      link=document.getElementById('link'), hint=document.getElementById('hint'), id=out.dataset.job, from=0, fails=0;
  function finish(ok){
    spin.style.display='none'; hint.style.display='none'; link.style.display='';
    statusEl.textContent=ok ? 'Restore complete — every volume was put back and the stack is up.'
                            : 'Restore FAILED — the output below says what went wrong and what (if anything) was put back.';
    document.querySelector('.note').className=ok?'flash':'banner';
  }
  function poll(){
    fetch('/admin/job?id='+encodeURIComponent(id)+'&from='+from,{cache:'no-store'})
      .then(function(r){ if(r.status===404){ throw 'gone'; } if(!r.ok || r.redirected){ throw 'down'; } return r.json(); })
      .then(function(j){
        fails=0; hint.style.display='none';
        if(j.out){ out.textContent+=j.out; out.scrollTop=out.scrollHeight; }
        from=j.next;
        if(j.done){ finish(j.ok); } else { setTimeout(poll, 2000); }
      })
      .catch(function(e){
        if(e==='gone'){ spin.style.display='none'; link.style.display=''; statusEl.textContent='The dashboard restarted, so this restore\'s progress is gone. Check the Backups page and hsctl status on the server.'; return; }
        fails++; if(fails>=40){ hint.style.display=''; } setTimeout(poll, 3000); // proxy down mid-restore; keep trying
      });
  }
  poll();
})();
</script>
</div></body></html>`

const helpTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Setup guide</title>
<style>{{css}}</style></head><body><div class="wrap">
<p><a href="/">← Dashboard</a></p>
<div class="md">{{.Body}}</div>
<p class="foot"><a href="/">← Back to dashboard</a></p>
</div></body></html>`

// runJS is the shared client for dashboard jobs (jobs.go). A button with data-slug starts that
// Command Center command (POST /admin/run); one with data-post/data-body POSTs there instead (the
// Apps and Updates pages). Either way the server answers with a job ID, and the page follows the
// job's output in #out by polling /admin/job — through a reload (#out's data-job), a dropped
// connection or Caddy restarting, because the job runs on the server regardless. data-confirm asks
// first (and, for /admin/run, sends the server-side confirmation token); data-reload refreshes the
// server-rendered panels once the job succeeds. It contains no template actions, so it passes
// through html/template as literal script text. (Careful editing: avoid literal "{{" or "}}" —
// they're template delimiters.)
const runJS = `
var out=document.getElementById('out'), jobStatus=document.getElementById('jobstatus');
function jobButtons(){ return document.querySelectorAll('button[data-slug],button[data-post]'); }
function setBusy(b){ jobButtons().forEach(function(x){ x.disabled=b; }); }
function sleep(ms){ return new Promise(function(r){ setTimeout(r,ms); }); }
function say(t){ if(jobStatus) jobStatus.textContent=t; }
async function runCmd(btn){
  var confirmMsg=btn.dataset.confirm;
  if(confirmMsg && !window.confirm(confirmMsg)) return;
  var url=btn.dataset.post||'/admin/run', body=btn.dataset.body||('slug='+encodeURIComponent(btn.dataset.slug));
  if(btn.dataset.slug && confirmMsg) body+='&confirm='+encodeURIComponent(btn.dataset.slug); // server-side guard for destructive ops
  setBusy(true);
  out.hidden=false;
  out.textContent='Starting…';
  out.scrollIntoView({behavior:'smooth',block:'center'});
  try{
    var res=await fetch(url,{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:body});
    var txt=await res.text();
    if(!res.ok){ out.textContent=txt; say('✖ Not started'); setBusy(false); return; }
    follow(JSON.parse(txt).id, btn.dataset.reload);
  }catch(e){ out.textContent='request failed: '+e; setBusy(false); }
}
async function follow(id, reload){
  var from=0;
  setBusy(true);
  out.hidden=false;
  out.textContent='';
  for(;;){
    var wait=1000;
    try{
      var r=await fetch('/admin/job?id='+encodeURIComponent(id)+'&from='+from,{cache:'no-store'});
      if(r.redirected || r.status===401){ say('You were signed out (did the dashboard restart?). The command keeps running on the server — sign in again to check on it.'); setBusy(false); return; }
      if(r.status===404){ say('The dashboard restarted, so this job\'s output is gone. The command itself kept running on the server.'); setBusy(false); return; }
      var j=await r.json();
      if(j.out){ out.textContent+=j.out; out.scrollTop=out.scrollHeight; }
      from=j.next;
      say((j.done?(j.ok?'✔ Finished: ':'✖ Failed: '):'⏳ Running: ')+j.title+' · '+j.elapsed);
      if(j.done){
        setBusy(false);
        if(j.ok && reload){ setTimeout(function(){ location.href=location.pathname; }, 1200); } // refresh server-rendered panels
        return;
      }
    }catch(e){
      say('⏳ Waiting for the server… (the command keeps running even while this page can\'t reach it)');
      wait=3000;
    }
    await sleep(wait);
  }
}
jobButtons().forEach(function(b){ b.addEventListener('click',function(){ runCmd(b); }); });
if(out.dataset.job){ follow(out.dataset.job, out.dataset.reload); }`

const commandsTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Commands</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>🧰 Command Center</h1>
<p class="sub">Everything the server's control tool can do — explained. Click a button and its output appears below. Commands keep running if you leave this page.</p>
{{if .Busy}}<div class="note" role="status">⏳ Running now: {{.Busy}}</div>{{end}}

{{range .Groups}}<h3>{{.Name}}</h3>
{{range .Cmds}}<div class="cmd">
  <h4>{{.Title}}{{if .IsDestructive}}<span class="badge destructive">destructive</span>{{else if .IsCaution}}<span class="badge caution">changes things</span>{{end}}{{if .NeedsRoot}}<span class="badge root">needs root</span>{{end}}{{if .Slow}}<span class="badge slow">may take a while</span>{{end}}</h4>
  <p>{{.Desc}}</p>
  <button class="btn {{.BtnClass}}" data-slug="{{.Slug}}" data-confirm="{{.Confirm}}">Run</button>
</div>
{{end}}{{end}}

<h3>Output</h3>
<p id="jobstatus" class="foot" style="margin-top:0" aria-live="polite"></p>
<div id="out" class="out" data-job="{{.JobID}}">Pick a command above — its output appears here as it runs.</div>
<p class="foot"><a href="/admin">← Admin</a> · <a href="/admin/terminal">Open a full terminal →</a></p>
<script>` + runJS + `</script>
</div></body></html>`

const updatesTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Updates</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>⬆️ Updates</h1>
<p class="sub">See which apps have a newer version, and apply updates with a click.</p>
{{if .Busy}}<div class="note" role="status">⏳ Running now: {{.Busy}}</div>{{end}}

{{if .Checked}}
<table>
<tr><th>App</th><th>Installed</th><th>Status</th><th></th></tr>
{{range .Rows}}<tr>
  <td>{{.Container}}</td>
  <td><code>{{.Image}}</code></td>
  <td>{{if .Stale}}<span class="tag bad">{{.State}}</span>{{if .Major}} <span class="badge caution">major upgrade</span>{{end}}{{else if eq .State "up to date"}}<span class="tag ok">up to date</span>{{else}}<span class="foot">{{.State}}</span>{{end}}</td>
  <td>{{if .Stale}}{{if .Major}}<button class="btn gray" data-post="/admin/updates/apply" data-body="name={{.Container}}" data-reload="1" data-confirm="{{.Container}} is a MAJOR upgrade ({{.State}}). These can need manual migration steps and are skipped by &quot;apply all&quot; on purpose. Only continue if you have tried it in the sandbox and have a fresh backup. Apply now?">Update…</button>{{else}}<button class="btn green" data-post="/admin/updates/apply" data-body="name={{.Container}}" data-reload="1" data-confirm="Update {{.Container}} now? It will restart briefly.">Update</button>{{end}}{{end}}</td>
</tr>{{end}}
</table>
<p class="foot">Checked {{.Age}} · registries are re-checked at apply time.</p>

{{if .RoutineN}}<p><button class="btn green" data-post="/admin/updates/apply" data-body="name=all" data-reload="1" data-confirm="Apply all routine updates now? The updated apps will restart briefly. (Major upgrades are skipped.)">Apply all routine updates ({{.RoutineN}})</button></p>{{end}}
{{if .StaleN}}<div class="note"><b>Routine vs major:</b> routine updates (new minor/patch versions) are safe to apply from here. <b>Major upgrades</b> — a new Nextcloud or database generation — can need manual steps, so they're never included in "apply all": try them in the sandbox first (see the README), make a fresh <a href="/admin/backup">backup</a>, then use their own Update button.</div>{{end}}
{{else}}
<div class="note">No check has run yet — click the button to compare every app against its registry (takes half a minute or so).</div>
{{end}}

<p><button class="btn" data-act="check">Check for updates</button></p>

<h3>Output</h3>
<p id="jobstatus" class="foot" style="margin-top:0" aria-live="polite"></p>
<div id="out" class="out" data-job="{{.JobID}}">Output appears here as a check or update runs.</div>
<p class="foot"><a href="/admin">← Admin</a></p>
<script>` + runJS + `
async function stream(url, body, reload){
  const out=document.getElementById('out');
  out.textContent='Working…\n';
  document.querySelectorAll('button[data-act],button[data-post]').forEach(b=>b.disabled=true);
  var ok=false;
  try{
    const res=await fetch(url,{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:body});
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
  finally{ document.querySelectorAll('button[data-act],button[data-post]').forEach(b=>b.disabled=false); }
  if(ok && reload){ setTimeout(function(){ location.href=location.pathname; }, 1200); }
}
document.querySelectorAll('button[data-act]').forEach(function(b){
  b.addEventListener('click',function(){ stream('/admin/updates/check','',true); });
});
</script>
</div></body></html>`

const devicesTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Drives</title>
<style>{{css}}</style></head><body><div class="wrap">
<h1>💽 Drives</h1>
<p class="sub">Storage attached to the server. Mount one to start using it (for example, as a backup destination).</p>
{{with .Flash}}{{if .Msg}}<div class="flash{{if .Err}} err{{end}}" role="status">{{.Msg}}</div>{{end}}{{end}}
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
  <td>{{if .Ejectable}}<form method="post" action="/admin/devices/unmount" style="margin:0" onsubmit="return confirm('Eject {{.Path}} (mounted at {{.Mountpoint}})? Make sure nothing is reading or writing it — no backup running.')"><input type="hidden" name="dev" value="{{.Path}}"><button class="btn gray">Eject</button></form>{{else if .Mountpoint}}<span class="foot">{{.Why}}</span>{{else if .Mountable}}<form method="post" action="/admin/devices/mount" style="margin:0 0 4px;display:flex;gap:6px;align-items:center"><input type="hidden" name="dev" value="{{.Path}}"><input class="in" name="target" value="{{.Suggest}}" title="Mount directory — edit to mount it wherever you want" style="width:150px;padding:6px"><button class="btn">Mount</button></form>{{if and $.GuardPath (not $.GuardOK)}}<form method="post" action="/admin/devices/mount" style="margin:0" onsubmit="return confirm('Mount {{.Path}} at {{$.GuardPath}} for backups?')"><input type="hidden" name="dev" value="{{.Path}}"><input type="hidden" name="target" value="{{$.GuardPath}}"><button class="btn green">Mount for backups</button></form>{{end}}{{else}}<span class="foot">{{.Why}}</span>{{end}}</td>
</tr>{{else}}<tr><td colspan="6">No drives detected.</td></tr>{{end}}
</table>

<div class="note"><b>You choose where each disk mounts</b> — the box is pre-filled with a suggestion under <code>{{.MountAt}}</code>, but edit it to mount wherever you like (same as a manual <code>mount</code> on the server). Mounts are <b>temporary</b> — they clear on reboot and no permanent (fstab) entry is written, matching the by-hand way the backup disk is attached. After mounting a disk for backups, set it as the destination on the <a href="/admin/backup">Backups</a> page.</div>
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
