package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// The first-run setup wizard. `hsctl install` leaves the box with Caddy up and the dashboard
// running but no app configured; the dashboard notices that (no core .env yet) and sends
// the admin here instead of the home page. Three steps on one URL:
//
//  1. GET  /setup      confirm the autodetected settings (IP, timezone, …)
//  2. POST /setup      save them, generate every service's .env + logins; show the logins ONCE
//  3. POST /setup/up   stream `hsctl up` (same exec path as the Command Center)
//
// Once the core .env files exist the wizard is closed: /setup just points back at the
// dashboard, and re-configuring goes through the Command Center / setup.conf as before.

// setupDone reports whether the stack has been configured (every core service has its .env).
func (s *uiServer) setupDone() bool {
	return len(missingEnvIn(s.repo)) == 0
}

type setupData struct {
	Apps    []appInfo
	Cfg     Config
	Err     string
	Secrets []Secret
	Step    int // 1 = form, 2 = logins + start
}

func (s *uiServer) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.setupDone() {
		http.Redirect(w, r, "/admin?msg="+
			"Setup is already complete. Settings live in setup.conf; secrets can be regenerated from the Command Center.", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		c := s.config()
		c.Normalize()
		render(w, setupTmpl, setupData{Cfg: c, Apps: listApps(s.repo, c, false), Step: 1})
		return
	}
	c, err := parseSetupForm(s.config(), r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		render(w, setupTmpl, setupData{Cfg: c, Apps: listApps(s.repo, c, false), Err: err.Error(), Step: 1})
		return
	}
	c.Normalize()
	if err := c.Save(s.repo); err != nil {
		http.Error(w, "saving setup.conf: "+err.Error(), http.StatusInternalServerError)
		return
	}
	secrets, err := c.Generate(s.repo, false)
	if err != nil {
		http.Error(w, "generating service configs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The bootstrap wrote caddy/.env from autodetected values; follow what the admin confirmed.
	if err := reconcileCaddyEnv(s.repo, c); err != nil {
		http.Error(w, "updating caddy/.env: "+err.Error(), http.StatusInternalServerError)
		return
	}
	uiLog.Info("setup wizard: configs generated", "ip", c.ServerIP, "from", remoteIP(r))
	render(w, setupTmpl, setupData{Cfg: c, Secrets: secrets, Step: 2})
}

// setupRunStatus is kept in memory so setup progress survives a dropped browser stream.
type setupRunStatus struct {
	Active bool   `json:"active"`
	Done   bool   `json:"done"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

type setupRunWriter struct{ s *uiServer }

func (w setupRunWriter) Write(p []byte) (int, error) {
	w.s.setupMu.Lock()
	w.s.setupSt.Output += string(p)
	w.s.setupMu.Unlock()
	return len(p), nil
}

// handleSetupUp starts `hsctl up` independently of the browser connection. The status endpoint
// below makes it safe for the page to reconnect while images are downloading.
func (s *uiServer) handleSetupUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	uiLog.Info("setup wizard: starting stack", "from", remoteIP(r))
	s.setupMu.Lock()
	if s.setupSt.Active {
		s.setupMu.Unlock()
		w.WriteHeader(http.StatusConflict)
		return
	}
	s.setupSt = setupRunStatus{Active: true}
	s.setupMu.Unlock()
	go func() {
		err := s.runHsctl(setupRunWriter{s: s}, "up")
		s.setupMu.Lock()
		s.setupSt.Active = false
		s.setupSt.Done = true
		s.setupSt.OK = err == nil
		if err != nil {
			s.setupSt.Output += fmt.Sprintf("\n[command exited with error: %v]\n", err)
		} else {
			s.setupSt.Output += "\n[done]\n"
		}
		s.setupMu.Unlock()
	}()
	w.WriteHeader(http.StatusAccepted)
}

func (s *uiServer) handleSetupUpStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	s.setupMu.Lock()
	st := s.setupSt
	s.setupMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// parseSetupForm applies the wizard's fields over base, validating the ones a typo would
// quietly break (the IP ends up in certificates and every app's trusted-domain list).
func parseSetupForm(base Config, r *http.Request) (Config, error) {
	c := base
	c.ServerIP = strings.TrimSpace(r.FormValue("server_ip"))
	c.TZ = strings.TrimSpace(r.FormValue("tz"))
	c.PiholeDNSBind = strings.TrimSpace(r.FormValue("pihole_dns_bind"))
	c.VWSignupsAllowed = r.FormValue("vw_signups") == "on"
	// Apps: every checked box is on; anything unchecked is switched off (browsers omit
	// unchecked boxes, so compute the complement against the registry).
	_ = r.ParseForm()
	on := map[string]bool{}
	for _, a := range r.Form["apps"] {
		on[a] = true
	}
	c.DisabledApps = nil
	for _, d := range appDirs() {
		if !on[d] {
			c.DisabledApps = append(c.DisabledApps, d)
		}
	}

	if len(c.DisabledApps) == len(appDirs()) {
		return c, fmt.Errorf("tick at least one app to run")
	}
	if ip := net.ParseIP(c.ServerIP); ip == nil || ip.To4() == nil {
		return c, fmt.Errorf("%q isn't a valid IPv4 address — use the server's LAN IP, e.g. 192.168.1.10", c.ServerIP)
	}
	if c.TZ == "" || strings.ContainsAny(c.TZ, " \t\n") {
		return c, fmt.Errorf("timezone must look like Europe/Brussels or Asia/Kolkata")
	}
	if c.PiholeDNSBind != "" && net.ParseIP(c.PiholeDNSBind) == nil {
		return c, fmt.Errorf("%q isn't a valid IP for Pi-hole to listen on (use 0.0.0.0 or the server IP)", c.PiholeDNSBind)
	}
	return c, nil
}

const setupTmpl = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Set up your home server</title>
<style>{{css}}
.steps{display:flex;gap:8px;margin:0 0 22px;font-size:14px;color:var(--muted)}
.steps b{color:var(--fg)}.steps span:not(:last-child):after{content:"›";margin-left:8px}
label{display:block;margin:14px 0 4px;font-weight:600}.hint{color:var(--muted);font-size:13.5px;margin:2px 0 0}
.secret{display:grid;grid-template-columns:1fr auto;gap:10px;align-items:center;background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:10px 12px;margin:8px 0}
.secret .l{color:var(--muted);font-size:13px}.secret code{font-size:15px;background:none;padding:0;word-break:break-all}
#done{display:none}
</style></head><body><div class="wrap">
<h1>🏠 Set up your home server</h1>
<div class="steps">
<span{{if eq .Step 1}}><b>1 Settings</b>{{else}}>1 Settings{{end}}</span>
<span{{if eq .Step 2}}><b>2 Your logins</b>{{else}}>2 Your logins{{end}}</span>
<span>3 Start</span><span>4 Certificate</span></div>

{{if eq .Step 1}}
<p class="sub">These were detected automatically — check them and press <b>Continue</b>. Everything can be changed later.</p>
{{if .Err}}<div class="banner">{{.Err}}</div>{{end}}
<form method="post" action="/setup" class="card">
  <label for="server_ip">Server address (LAN IP)</label>
  <input class="in" id="server_ip" name="server_ip" value="{{.Cfg.ServerIP}}" required>
  <p class="hint">The address everyone will use, e.g. <code>https://{{.Cfg.ServerIP}}</code>. Give the server a fixed IP (a “DHCP reservation”) in your router so it never changes.</p>

  <label for="tz">Timezone</label>
  <input class="in" id="tz" name="tz" value="{{.Cfg.TZ}}" required>
  <p class="hint">Used for Pi-hole's statistics and backup times, e.g. <code>Europe/Brussels</code>.</p>

  <label for="pihole_dns_bind">Pi-hole DNS listen address</label>
  <input class="in" id="pihole_dns_bind" name="pihole_dns_bind" value="{{.Cfg.PiholeDNSBind}}">
  <p class="hint">Leave as is. <code>0.0.0.0</code> = all interfaces; the server IP is suggested when something on this machine already uses port 53.</p>

  <label>Apps to run</label>
  {{range .Apps}}<label style="font-weight:400;margin:4px 0"><input type="checkbox" name="apps" value="{{.Dir}}" {{if .Enabled}}checked{{end}}> {{.Icon}} <b>{{.Name}}</b> <span class="hint" style="display:inline">— {{.Desc}}</span></label>
  {{end}}<p class="hint">Untick anything you don't want. You can switch apps on or off later from Admin → Apps; nothing is deleted.</p>

  <label><input type="checkbox" name="vw_signups" {{if .Cfg.VWSignupsAllowed}}checked{{end}}> Let anyone on the network create a Vaultwarden (password manager) account</label>
  <p class="hint">Handy while your family signs up. You can switch it off later for invitation-only.</p>

  <p style="margin-top:20px"><button class="btn" type="submit">Continue →</button></p>
</form>
<p class="foot">Dashboard port: {{.Cfg.UIPort}} (set by <code>hsctl install</code>). · <a href="/help">Setup guide</a></p>

{{else}}
<p class="sub">Settings saved and every app configured. These are the <b>admin logins it just created</b>.</p>
<div class="note"><b>Save these now</b> — into a notes app for the moment, then into Vaultwarden once it's running. The Vaultwarden admin token is only ever shown here; the others can be shown again from the Command Center (“Show logins”).</div>
{{range .Secrets}}<div class="secret"><div><div class="l">{{.Label}}</div><code>{{.Value}}</code></div>
<button class="btn gray" type="button" data-copy="{{.Value}}">Copy</button></div>{{end}}
{{if not .Secrets}}<p>(No new logins — the configs already existed, so nothing was regenerated.)</p>{{end}}

<h3 style="margin-top:26px">3 · Start everything</h3>
<p class="hint">Downloads the apps the first time (a few minutes on a slow connection) and starts them. Watch the progress below.</p>
<p><label style="display:inline;font-weight:400"><input type="checkbox" id="saved"> I've saved the logins above</label></p>
<p><button class="btn green" id="start" disabled>Start everything</button></p>
<div id="out" class="out">Waiting…</div>

<div id="done">
<h3 style="margin-top:26px">4 · Install the certificate on each device</h3>
<p>Your server makes its own HTTPS certificate. Each phone or laptop trusts it <b>once</b> — then the warnings disappear and the Bitwarden / Nextcloud apps connect.</p>
<p><a class="btn" href="/root.crt">Download root.crt</a> <a class="btn gray" href="/help">How to install it (per device)</a></p>
<p>Then you're done: <a href="/"><b>open the dashboard →</b></a> (Nextcloud takes a minute or two on its first start).</p>
</div>

<script>
document.querySelectorAll('button[data-copy]').forEach(function(b){
  b.addEventListener('click',function(){
    navigator.clipboard.writeText(b.dataset.copy).then(function(){ b.textContent='Copied'; setTimeout(function(){ b.textContent='Copy'; },1500); });
  });
});
var saved=document.getElementById('saved'), start=document.getElementById('start');
saved.addEventListener('change',function(){ start.disabled=!saved.checked; });
start.addEventListener('click',async function(){
  var out=document.getElementById('out');
  start.disabled=true; saved.disabled=true; out.textContent='Starting…\n';
  var shown=false;
  function poll(){
    fetch('/setup/up/status').then(function(res){
      if(!res.ok) throw new Error('status '+res.status);
      return res.json();
    }).then(function(st){
      out.textContent=st.output; out.scrollTop=out.scrollHeight;
      if(st.active){ setTimeout(poll,1000); return; }
      if(st.done && st.ok){ document.getElementById('done').style.display='block'; document.getElementById('done').scrollIntoView({behavior:'smooth'}); return; }
      start.disabled=false; saved.disabled=false;
      if(!shown){ out.textContent+='\n\nSomething went wrong — fix the problem above and press Start again (safe to repeat).'; shown=true; }
    }).catch(function(){ setTimeout(poll,1500); });
  }
  try{
    const res=await fetch('/setup/up',{method:'POST'});
    if(!res.ok && res.status!==409) throw new Error('start '+res.status);
  }catch(e){ out.textContent+='\nConnection interrupted; checking whether the server started the job…\n'; }
  poll();
});
</script>
{{end}}
</div></body></html>`
