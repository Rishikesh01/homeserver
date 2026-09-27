package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---- renewal loop -------------------------------------------------------------

// certRenewLoop keeps your domain's certificate fresh while the dashboard runs: a first check
// shortly after it starts (so a box that was switched off for weeks catches up), then every
// certRenewEvery. lego only contacts Let's Encrypt when the certificate is actually due, so
// most checks just confirm there's nothing to do. Without a domain configured it does nothing.
func (s *uiServer) certRenewLoop() {
	time.Sleep(2 * time.Minute) // let Docker and the stack settle after a boot
	for {
		s.renewCertOnce()
		time.Sleep(certRenewEvery)
	}
}

func (s *uiServer) renewCertOnce() {
	var out bytes.Buffer
	renewed, err := renewCert(s.repo, &out)
	switch {
	case err != nil:
		uiLog.Warn("certificate check failed — retried automatically; `hsctl cert issue` shows the full log",
			"err", err, "log", lastLines(out.String(), 15))
	case renewed:
		uiLog.Info("certificate renewed", "domain", s.config().Domain)
	}
}

// lastLines keeps the tail of a command's output for a log line.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---- dashboard: /admin/cert --------------------------------------------------------

type certPageData struct {
	Cfg       Config
	St        certStatus
	CredKeys  []string // names saved in .acme-env — never the values
	Providers []string // providers with known credential names, for the form's suggestions
	Msg, Err  string
}

func (s *uiServer) handleCert(w http.ResponseWriter, r *http.Request) {
	c := s.config()
	d := certPageData{Cfg: c, St: loadCertStatus(s.repo, c), Msg: r.URL.Query().Get("msg"), Err: r.URL.Query().Get("err")}
	if creds, err := acmeCredentials(s.repo); err == nil {
		for k := range creds {
			d.CredKeys = append(d.CredKeys, k)
		}
		sort.Strings(d.CredKeys)
	}
	for p := range dnsCredentialHints {
		d.Providers = append(d.Providers, p)
	}
	sort.Strings(d.Providers)
	render(w, certTmpl, d)
}

var envKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// parseCredentialLines reads the form's KEY=value lines. Blank lines and #comments are
// skipped; anything else that isn't KEY=value is an error, so a pasted token can't silently
// land under the wrong name.
func parseCredentialLines(text string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !envKeyRE.MatchString(k) || v == "" {
			// Name the line, never echo it: a token pasted without its KEY= would otherwise
			// end up in the page's URL.
			return nil, fmt.Errorf("credentials line %d isn't KEY=value (like CF_DNS_API_TOKEN=abc123) — nothing was saved", i+1)
		}
		out[k] = v
	}
	return out, nil
}

// handleCertConfig saves the form: domain, provider, email, and any credentials typed in
// (merged into .acme-env; blank keeps what's there). Getting the certificate is a separate
// button, so its progress can stream.
func (s *uiServer) handleCertConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/cert", http.StatusSeeOther)
		return
	}
	fail := func(err error) {
		http.Redirect(w, r, "/admin/cert?err="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
	}
	c := s.config()
	domain, err := normalizeDomain(r.FormValue("domain"))
	if err != nil {
		fail(err)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.FormValue("dns")))
	if !providerRE.MatchString(provider) {
		fail(fmt.Errorf("DNS provider: give lego's provider code, e.g. cloudflare"))
		return
	}
	creds, err := parseCredentialLines(r.FormValue("creds"))
	if err != nil {
		fail(err)
		return
	}
	// Check the provider exists (and learn its variables for a fresh .acme-env) only when it
	// changed — it runs a container.
	help := ""
	envPath := filepath.Join(s.repo, acmeEnvFile)
	if provider != c.DNSProvider || !fileExists(envPath) {
		if help, err = dnsHelp(s.repo, provider); err != nil {
			fail(err)
			return
		}
	}
	c.Domain, c.DNSProvider = domain, provider
	c.ACMEEmail = strings.TrimSpace(r.FormValue("email"))
	c.ACMEServer = strings.TrimSpace(r.FormValue("server"))
	if err := c.Save(s.repo); err != nil {
		fail(err)
		return
	}
	if !fileExists(envPath) {
		if err := writeFile0600(envPath, acmeEnvTemplate(c, help)); err != nil {
			fail(err)
			return
		}
	}
	keys := make([]string, 0, len(creds))
	for k := range creds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := upsertEnvKey(envPath, k, creds[k]); err != nil {
			fail(err)
			return
		}
	}
	uiLog.Info("domain settings saved", "domain", domain, "provider", provider, "credentials", strings.Join(keys, ","), "from", remoteIP(r))
	msg := "Saved. Now press “Get certificate”."
	if have, _ := acmeCredentials(s.repo); len(have) == 0 {
		msg = "Saved — but there are no DNS API credentials yet: add them below, then press “Get certificate”."
	}
	http.Redirect(w, r, "/admin/cert?msg="+template.URLQueryEscaper(msg), http.StatusSeeOther)
}
