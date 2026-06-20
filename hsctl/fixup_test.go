package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetEnvKV(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "vaultwarden"), 0700); err != nil {
		t.Fatal(err)
	}
	// A realistic vaultwarden/.env: the Argon2 token line has doubled "$$" that MUST survive.
	token := "VW_ADMIN_TOKEN=$$argon2id$$v=19$$m=65540,t=3,p=4$$c2FsdA$$aGFzaA"
	orig := "VW_DOMAIN=https://192.168.0.150:8443\n" + token + "\nVW_SIGNUPS_ALLOWED=true\nVW_HTTP_PORT=8082\n"
	envPath := filepath.Join(repo, "vaultwarden", ".env")
	if err := os.WriteFile(envPath, []byte(orig), 0600); err != nil {
		t.Fatal(err)
	}

	if err := setEnvKV(repo, "vaultwarden/.env", "VW_DOMAIN", "https://cloudbox:8443"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(envPath)
	s := string(got)
	if !strings.Contains(s, "VW_DOMAIN=https://cloudbox:8443\n") {
		t.Errorf("VW_DOMAIN not updated:\n%s", s)
	}
	if !strings.Contains(s, token) {
		t.Errorf("Argon2 token line was disturbed (the $$ must be untouched):\n%s", s)
	}
	if strings.Contains(s, "192.168.0.150") {
		t.Errorf("old domain still present:\n%s", s)
	}
	// Other keys and the trailing newline are intact.
	if !strings.Contains(s, "VW_HTTP_PORT=8082\n") || !strings.HasSuffix(s, "\n") {
		t.Errorf("other lines / trailing newline disturbed:\n%q", s)
	}

	// Appends a missing key without a trailing blank line explosion.
	if err := setEnvKV(repo, "vaultwarden/.env", "VW_NEW_KEY", "x"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(envPath)
	if !strings.Contains(string(got), "VW_NEW_KEY=x\n") {
		t.Errorf("missing key not appended:\n%s", got)
	}
}

func TestPatchVWConfigDomain(t *testing.T) {
	dir := t.TempDir()

	// Absent file: no-op, no error (the common case — image has no config.json).
	if err := patchVWConfigDomain(filepath.Join(dir, "nope.json"), "https://x"); err != nil {
		t.Errorf("absent file should be a no-op, got: %v", err)
	}

	// File without a "domain" key: left untouched.
	noDomain := filepath.Join(dir, "a.json")
	os.WriteFile(noDomain, []byte(`{"signups_allowed":true}`), 0600)
	if err := patchVWConfigDomain(noDomain, "https://cloudbox:8443"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(noDomain); strings.Contains(string(b), "cloudbox") {
		t.Errorf("should not add a domain key when absent: %s", b)
	}

	// File WITH a "domain" key: updated, other keys preserved.
	withDomain := filepath.Join(dir, "b.json")
	os.WriteFile(withDomain, []byte(`{"domain":"https://192.168.0.150:8443","signups_allowed":false}`), 0600)
	if err := patchVWConfigDomain(withDomain, "https://cloudbox:8443"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(withDomain)
	if !strings.Contains(string(b), `"domain": "https://cloudbox:8443"`) {
		t.Errorf("domain not updated: %s", b)
	}
	if !strings.Contains(string(b), `"signups_allowed": false`) {
		t.Errorf("other key not preserved: %s", b)
	}
}

func TestIdentityFor(t *testing.T) {
	id := identityFor("cloudbox")
	if id.Host != "cloudbox" || id.VWURL != "https://cloudbox:8443" || id.NCURL != "https://cloudbox:8444" {
		t.Errorf("identityFor(cloudbox) = %+v", id)
	}
	if got := homeIdentity(Config{ServerIP: "192.168.0.150"}); got.VWURL != "https://192.168.0.150:8443" {
		t.Errorf("homeIdentity = %+v", got)
	}
}

func TestNextTrustedDomainIndex(t *testing.T) {
	domains := []string{"localhost", "192.168.0.150", "cloud.lan"}
	if got := nextTrustedDomainIndex(domains, "cloudbox"); got != 3 {
		t.Errorf("new host should append at index 3, got %d", got)
	}
	if got := nextTrustedDomainIndex(domains, "cloud.lan"); got != -1 {
		t.Errorf("existing host should return -1, got %d", got)
	}
}
