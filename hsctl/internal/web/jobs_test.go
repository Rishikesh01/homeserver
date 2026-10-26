//go:build integration

package web_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
)

// TestCommands runs Command Center cards as background jobs of the real hsctl: the page gets a job
// to follow, a destructive card needs its confirmation token, and nothing starts while another
// operation holds the lock.
func TestCommands(t *testing.T) {
	itest.Hsctl(t)
	d := newDash(t, configured(t), nil)
	d.login()
	cases := []struct {
		name     string
		form     url.Values
		hold     bool
		wantCode int
		wantOut  []string
		wantOK   bool
	}{
		{"show status", url.Values{"slug": {"status"}}, false, 200, []string{"$ hsctl status", "no stack containers yet", "[done]"}, true},
		{"show logins", url.Values{"slug": {"secrets-show"}}, false, 200, []string{"Pi-hole admin:"}, true},
		{"unknown card", url.Values{"slug": {"rm-rf"}}, false, 404, nil, false},
		{"destructive without its token", url.Values{"slug": {"down-volumes"}}, false, 403, nil, false},
		{"another operation running", url.Values{"slug": {"status"}}, true, 409, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hold {
				l, err := d.lock.Acquire("test holder", 0)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Release()
			}
			if tc.wantCode != 200 {
				if resp, body := d.post("/admin/run", tc.form); resp.StatusCode != tc.wantCode {
					t.Fatalf("= %d %s", resp.StatusCode, body)
				}
				return
			}
			st := d.waitJob(d.startJob("/admin/run", tc.form))
			if st.OK != tc.wantOK {
				t.Errorf("ok = %v\n%s", st.OK, st.Out)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(st.Out, w) {
					t.Errorf("output lacks %q:\n%s", w, st.Out)
				}
			}
		})
	}
	if resp, _ := d.get("/admin/run"); resp.StatusCode != 405 {
		t.Errorf("GET /admin/run = %d", resp.StatusCode)
	}
}

// TestApps switches an app off and on from the Apps page: validated before anything runs, then
// done by the real `hsctl apps` as a job.
func TestApps(t *testing.T) {
	itest.Hsctl(t)
	itest.NeedRoot(t)
	itest.NeedDocker(t)
	d := newDash(t, configured(t), nil)
	d.login()
	cases := []struct {
		name     string
		method   string
		form     url.Values
		wantCode int
		wantOut  string
		running  bool
	}{
		{"GET goes back to the page", "GET", nil, 303, "", false},
		{"not an app", "POST", url.Values{"app": {"../../etc"}, "action": {"disable"}}, 400, "", false},
		{"the proxy can't be switched", "POST", url.Values{"app": {"caddy"}, "action": {"disable"}}, 400, "", false},
		{"unknown action", "POST", url.Values{"app": {"stirling"}, "action": {"rm"}}, 400, "", false},
		{"switch on", "POST", url.Values{"app": {"imagetools"}, "action": {"enable"}}, 200, "imagetools is on.", true},
		{"switch off", "POST", url.Values{"app": {"imagetools"}, "action": {"disable"}}, 200, "imagetools is off.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantCode != 200 {
				if resp, body := d.do(tc.method, "/admin/apps/toggle", tc.form, nil); resp.StatusCode != tc.wantCode {
					t.Fatalf("= %d %s", resp.StatusCode, body)
				}
				return
			}
			st := d.waitJob(d.startJob("/admin/apps/toggle", tc.form))
			if !st.OK || !strings.Contains(st.Out, tc.wantOut) {
				t.Fatalf("ok=%v\n%s", st.OK, st.Out)
			}
			if got := docker.New("").ContainerState("imagetools") == "running"; got != tc.running {
				t.Errorf("imagetools running = %v, want %v", got, tc.running)
			}
		})
	}
}

// TestUpdates checks a real stack against a real registry from the Updates page: a major release is
// flagged and left out of "apply all", the apply endpoint only takes what the last check found, and
// "apply all" runs the real `hsctl updates --apply` as a job.
func TestUpdates(t *testing.T) {
	itest.Hsctl(t)
	reg := itest.StartRegistry(t)
	itest.Pull(t, itest.Busybox)
	repo := configured(t)
	for app, tags := range map[string][2]string{"vaultwarden": {"1.0.0", "2.0.0"}, "it-tools": {"2024.10.22-7ca5933", "2024.11.05-abc1234"}} {
		running := reg.Push(t, itest.Busybox, "hs/"+app+":"+tags[0])
		reg.Push(t, itest.Busybox, "hs/"+app+":"+tags[1])
		key := map[string]string{"vaultwarden": "VAULTWARDEN_IMAGE", "it-tools": "ITTOOLS_IMAGE"}[app]
		if err := envfile.Upsert(repo.Path(app, ".env"), key, running); err != nil {
			t.Fatal(err)
		}
		itest.WriteFile(t, repo.Root, app+"/docker-compose.override.yml",
			"services:\n  "+app+":\n    command: [\"sleep\", \"3600\"]\n    healthcheck: {disable: true}\n")
	}
	dk := docker.New(repo.Root)
	if err := dk.EnsureNetwork("homeserver-edge"); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"vaultwarden", "it-tools"} {
		if out, err := dk.Combined(repo.Path(app), "compose", "up", "-d"); err != nil {
			t.Fatalf("up %s: %v\n%s", app, err, out)
		}
	}
	d := newDash(t, repo, &registry.Client{HTTP: reg.HTTP})
	d.login()
	steps := []struct {
		name     string
		do       func() (int, string)
		wantCode int
		want     []string
		notWant  string
	}{
		{"nothing to apply before a check", func() (int, string) {
			resp, body := d.post("/admin/updates/apply", url.Values{"name": {"vaultwarden"}})
			return resp.StatusCode, body
		}, 404, nil, ""},
		{"check", func() (int, string) { resp, body := d.post("/admin/updates/check", nil); return resp.StatusCode, body },
			200, []string{"vaultwarden", "NEWER RELEASE 2.0.0", "NEWER RELEASE 2024.11.05-abc1234", "[done]"}, ""},
		{"the page", func() (int, string) { resp, body := d.get("/admin/updates"); return resp.StatusCode, body },
			200, []string{"major upgrade", "Apply all routine updates (1)"}, "No check has run yet"},
		{"an app the check didn't find", func() (int, string) {
			resp, body := d.post("/admin/updates/apply", url.Values{"name": {"caddy; rm -rf /"}})
			return resp.StatusCode, body
		}, 404, nil, ""},
		{"GET", func() (int, string) { resp, body := d.get("/admin/updates/apply"); return resp.StatusCode, body }, 405, nil, ""},
		{"while another operation runs", func() (int, string) {
			l, err := d.lock.Acquire("test holder", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Release()
			resp, body := d.post("/admin/updates/apply", url.Values{"name": {"all"}})
			return resp.StatusCode, body
		}, 409, []string{"test holder"}, ""},
		{"apply all", func() (int, string) {
			st := d.waitJob(d.startJob("/admin/updates/apply", url.Values{"name": {"all"}}))
			if !st.OK {
				return 500, st.Out
			}
			return 200, st.Out
		}, 200, []string{"skipping vaultwarden: 1.0.0 -> 2.0.0 is a major upgrade", "pinned ITTOOLS_IMAGE="}, ""},
		{"the result is applied", func() (int, string) {
			kv, _ := envfile.Read(repo.Path("it-tools/.env"))
			return 200, fmt.Sprintf("env %s runs %s vw %s", kv["ITTOOLS_IMAGE"], dk.ContainerImage("it-tools"), dk.ContainerImage("vaultwarden"))
		}, 200, []string{"env " + reg.Host + "/hs/it-tools:2024.11.05-abc1234 runs " + reg.Host + "/hs/it-tools:2024.11.05-abc1234",
			"vw " + reg.Host + "/hs/vaultwarden:1.0.0"}, ""},
		{"an apply clears the old results", func() (int, string) { resp, body := d.get("/admin/updates"); return resp.StatusCode, body },
			200, []string{"No check has run yet"}, ""},
	}
	for _, st := range steps {
		code, body := st.do()
		if code != st.wantCode {
			t.Fatalf("%s: %d\n%s", st.name, code, body)
		}
		for _, w := range st.want {
			if !strings.Contains(body, w) {
				t.Fatalf("%s: lacks %q\n%s", st.name, w, body)
			}
		}
		if st.notWant != "" && strings.Contains(body, st.notWant) {
			t.Fatalf("%s: shows %q", st.name, st.notWant)
		}
	}
}

// TestBackupPages: the Backups page's forms — destination, retention, password — save what's valid
// and say why not otherwise; the restore only starts once RESTORE is typed, as a job the progress
// page follows.
func TestBackupPages(t *testing.T) {
	itest.Hsctl(t)
	itest.NeedRestic(t)
	repo := configured(t)
	d := newDash(t, repo, nil)
	d.login()
	flash := func(path string, form url.Values) (int, string) {
		resp, body := d.post(path, form)
		if resp.StatusCode != 303 {
			return resp.StatusCode, body
		}
		resp, body = d.get(resp.Header.Get("Location"))
		return resp.StatusCode, body
	}
	steps := []struct {
		name  string
		do    func() (int, string)
		want  []string
		check func() string
	}{
		{"a retention that keeps nothing", func() (int, string) {
			return flash("/admin/backup/config", url.Values{"keep_daily": {""}, "keep_weekly": {""}, "keep_monthly": {""}, "keep_rest": {""}})
		}, []string{"Not saved —"}, nil},
		{"a new retention", func() (int, string) {
			return flash("/admin/backup/config", url.Values{"keep_daily": {"14"}, "keep_weekly": {"4"}, "keep_monthly": {"12"}, "keep_rest": {""}})
		}, []string{"Saved."}, func() string {
			if kv, _ := envfile.Read(repo.Path(paths.BackupConf)); kv["RETENTION"] != "--keep-daily 14 --keep-weekly 4 --keep-monthly 12" {
				return fmt.Sprintf("backup.conf: %v", kv)
			}
			return ""
		}},
		{"an external disk", func() (int, string) {
			return flash("/admin/backup/config", url.Values{"repo": {"/mnt/usb/restic"}})
		}, []string{"/mnt/usb"}, func() string {
			if kv, _ := envfile.Read(repo.Path(paths.BackupConf)); kv["REQUIRE_MOUNT"] != "/mnt/usb" {
				return fmt.Sprintf("the guard didn't follow: %v", kv)
			}
			return ""
		}},
		{"a notification address that isn't a URL", func() (int, string) {
			return flash("/admin/backup/config", url.Values{"notify": {"ntfy.sh/x"}})
		}, []string{"must be an http(s):// URL"}, nil},
		{"passwords that don't match", func() (int, string) {
			return flash("/admin/backup/password", url.Values{"password": {"a-long-password-1"}, "confirm": {"a-long-password-2"}})
		}, []string{"don&#39;t match"}, nil},
		{"back to a local destination, guard off", func() (int, string) {
			return flash("/admin/backup/config", url.Values{"repo": {repo.Path("backups", "restic")}, "require_mount": {""}})
		}, []string{"Saved."}, nil},
		{"too short", func() (int, string) {
			return flash("/admin/backup/password", url.Values{"password": {"short"}, "confirm": {"short"}})
		}, []string{"at least 12"}, nil},
		{"set before init", func() (int, string) {
			return flash("/admin/backup/password", url.Values{"password": {"a-long-password-1"}, "confirm": {"a-long-password-1"}})
		}, []string{"Saved the backup password"}, func() string {
			if strings.TrimSpace(itest.ReadFile(repo.Path(paths.ResticPassword))) != "a-long-password-1" {
				return "not saved"
			}
			return ""
		}},
		{"restore without typing RESTORE", func() (int, string) {
			return flash("/admin/backup/restore", url.Values{"confirm": {"yes"}})
		}, []string{"Type RESTORE to confirm"}, nil},
		{"restore with a snapshot ID that isn't one", func() (int, string) {
			return flash("/admin/backup/restore", url.Values{"confirm": {"RESTORE"}, "snapshot": {"--target=/etc"}})
		}, []string{"look like a snapshot ID"}, nil},
		{"restore", func() (int, string) {
			resp, body := d.post("/admin/backup/restore", url.Values{"confirm": {"RESTORE"}})
			loc := resp.Header.Get("Location")
			if resp.StatusCode != 303 || !strings.HasPrefix(loc, "/admin/backup/restore/progress?id=") {
				return resp.StatusCode, body
			}
			st := d.waitJob(strings.TrimPrefix(loc, "/admin/backup/restore/progress?id="))
			return 200, st.Out
		}, []string{"$ hsctl backup restore --into-volumes --yes", "repository does not exist", "[command exited with error"}, nil},
	}
	for _, st := range steps {
		code, body := st.do()
		if code != 200 {
			t.Fatalf("%s: %d\n%s", st.name, code, body)
		}
		for _, w := range st.want {
			if !strings.Contains(body, w) {
				t.Fatalf("%s: lacks %q\n%s", st.name, w, body)
			}
		}
		if st.check != nil {
			if msg := st.check(); msg != "" {
				t.Fatalf("%s: %s", st.name, msg)
			}
		}
	}
}
