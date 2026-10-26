//go:build integration

package provision_test

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
)

var base = config.Settings{ServerIP: "10.0.0.5", TZ: "Etc/UTC", ACMEEmail: "a@b", UIPort: 8088,
	PiholeDNSBind: "0.0.0.0", VWSignupsAllowed: true}

func newService(t *testing.T, repo paths.Repo) (*provision.Service, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = &out, &out
	s := provision.New(repo, dk)
	s.Out, s.Err = &out, &out
	return s, &out
}

func env(t *testing.T, repo paths.Repo, rel string) map[string]string {
	t.Helper()
	kv, err := envfile.Read(repo.Path(rel))
	if err != nil {
		t.Fatal(err)
	}
	return kv
}

// TestGenerate: a fresh checkout gets every service's .env with fresh secrets, Pi-hole's list and a
// handout — private, and the admin's even when root wrote them — and the new logins are reported.
func TestGenerate(t *testing.T) {
	repo := itest.Checkout(t)
	s, _ := newService(t, repo)
	res, err := s.Generate(base, false)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, sec := range res.Secrets {
		labels = append(labels, sec.Label)
		if len(sec.Value) < 16 {
			t.Errorf("%s: weak secret %q", sec.Label, sec.Value)
		}
	}
	vw := env(t, repo, "vaultwarden/.env")
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"logins reported", strings.Join(labels, "|"), "Vaultwarden /admin token (SAVE — not recoverable):|Nextcloud (user 'admin'):|Pi-hole admin:"},
		{"VW domain", vw["VW_DOMAIN"], "https://10.0.0.5:8443"},
		{"VW token stored hashed and escaped", strings.HasPrefix(vw["VW_ADMIN_TOKEN"], "$$argon2id$$"), true},
		{"VW signups", vw["VW_SIGNUPS_ALLOWED"], "true"},
		{"NC trusted domains", env(t, repo, "nextcloud/.env")["NC_TRUSTED_DOMAINS"], "10.0.0.5"},
		{"NC passwords differ", env(t, repo, "nextcloud/.env")["POSTGRES_PASSWORD"] != env(t, repo, "nextcloud/.env")["REDIS_PASSWORD"], true},
		{"Pi-hole bind", env(t, repo, "pihole/.env")["PIHOLE_DNS_BIND"], "0.0.0.0"},
		{"Caddy upstream", env(t, repo, "caddy/.env")["HOME_UPSTREAM"], "host.docker.internal:8088"},
		{"Caddy file is CaddyEnv", itest.ReadFile(repo.Path("caddy/.env")), provision.CaddyEnv(base)},
		{"handout", strings.Contains(itest.ReadFile(repo.Path(paths.Welcome)), "https://10.0.0.5:8443"), true},
		{"Pi-hole list", strings.HasPrefix(itest.ReadFile(repo.Path(paths.PiholeList)), "# Pi-hole local DNS records"), true},
		{"configured", s.Configured(), true},
		{"nothing missing", len(s.MissingEnv(base)), 0},
		{"nothing changed on a first run", len(res.Changed), 0},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	for _, rel := range paths.EnvFiles {
		fi, err := os.Stat(repo.Path(rel))
		if err != nil || fi.Mode().Perm() != 0600 {
			t.Errorf("%s: %v %v, want 0600", rel, fi.Mode(), err)
		}
		if os.Geteuid() == 0 && itest.Owner(t, repo.Path(rel)) != "1000:1000" {
			t.Errorf("%s owned by %s, want the admin", rel, itest.Owner(t, repo.Path(rel)))
		}
	}
}

// TestRerunAppliesSettings: running setup again with new settings reaches the existing .env files
// (non-secret keys only, hand-set values left alone), reports each change and keeps every secret.
func TestRerunAppliesSettings(t *testing.T) {
	cases := []struct {
		name   string
		before map[string]string // hand edits before the re-run: "file:KEY" -> value
		change func(*config.Settings)
		want   map[string]string
	}{
		{"signups off", nil, func(c *config.Settings) { c.VWSignupsAllowed = false },
			map[string]string{"vaultwarden/.env:VW_SIGNUPS_ALLOWED": "false"}},
		{"timezone", nil, func(c *config.Settings) { c.TZ = "Europe/Berlin" },
			map[string]string{"pihole/.env:TZ": "Europe/Berlin"}},
		{"dashboard port", nil, func(c *config.Settings) { c.UIPort = 9000 },
			map[string]string{"caddy/.env:HOME_UPSTREAM": "host.docker.internal:9000"}},
		{"new server IP", nil, func(c *config.Settings) { c.ServerIP = "10.0.0.9" }, map[string]string{
			"caddy/.env:SERVER_IP":              "10.0.0.9",
			"vaultwarden/.env:VW_DOMAIN":        "https://10.0.0.9:8443",
			"nextcloud/.env:NC_TRUSTED_DOMAINS": "10.0.0.9",
		}},
		{"new IP keeps hand-set names", map[string]string{
			"vaultwarden/.env:VW_DOMAIN":        "https://vault.example.com",
			"nextcloud/.env:NC_TRUSTED_DOMAINS": "cloud.example.com 10.0.0.5",
			"caddy/.env:HOME_UPSTREAM":          "dashboard:8088",
		}, func(c *config.Settings) { c.ServerIP, c.UIPort = "10.0.0.9", 9000 }, map[string]string{
			"vaultwarden/.env:VW_DOMAIN":        "https://vault.example.com",
			"nextcloud/.env:NC_TRUSTED_DOMAINS": "cloud.example.com 10.0.0.9",
			"caddy/.env:HOME_UPSTREAM":          "dashboard:8088",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Checkout(t)
			s, _ := newService(t, repo)
			c := base
			if _, err := s.Generate(c, false); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.before {
				file, key, _ := strings.Cut(k, ":")
				if err := envfile.Set(repo.Path(file), key, v); err != nil {
					t.Fatal(err)
				}
			}
			secrets := env(t, repo, "nextcloud/.env")["POSTGRES_PASSWORD"] + env(t, repo, "vaultwarden/.env")["VW_ADMIN_TOKEN"]
			tc.change(&c)
			res, err := s.Generate(c, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Secrets) != 0 || len(res.Changed) == 0 {
				t.Errorf("secrets %v, changed %v", res.Secrets, res.Changed)
			}
			for k, want := range tc.want {
				file, key, _ := strings.Cut(k, ":")
				if got := env(t, repo, file)[key]; got != want {
					t.Errorf("%s %s = %q, want %q", file, key, got, want)
				}
			}
			if after := env(t, repo, "nextcloud/.env")["POSTGRES_PASSWORD"] + env(t, repo, "vaultwarden/.env")["VW_ADMIN_TOKEN"]; after != secrets {
				t.Error("a secret changed")
			}
		})
	}
}

// TestForce: --force rotates secrets in place — hand edits and Pi-hole's records survive — except
// the ones existing data depends on, which are kept and reported.
func TestForce(t *testing.T) {
	itest.NeedDocker(t)
	cases := []struct {
		name        string
		volumes     []string // existing data
		rotated     []string // "file:KEY"
		kept        []string
		wantKeptMsg []string
	}{
		{"no data yet", nil,
			[]string{"vaultwarden/.env:VW_ADMIN_TOKEN", "pihole/.env:PIHOLE_PASSWORD", "nextcloud/.env:REDIS_PASSWORD",
				"nextcloud/.env:POSTGRES_PASSWORD", "nextcloud/.env:NC_ADMIN_PASSWORD"},
			[]string{"pihole/.env:MY_NOTE", "caddy/.env:SERVER_IP"}, nil},
		{"Nextcloud installed", []string{"nextcloud_db-data-v18", "nextcloud_nc-html"},
			[]string{"vaultwarden/.env:VW_ADMIN_TOKEN", "nextcloud/.env:REDIS_PASSWORD"},
			[]string{"nextcloud/.env:POSTGRES_PASSWORD", "nextcloud/.env:NC_ADMIN_PASSWORD"},
			[]string{"POSTGRES_PASSWORD", "NC_ADMIN_PASSWORD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Checkout(t)
			for _, v := range tc.volumes {
				itest.Docker(t, "volume", "create", v)
				v := v
				t.Cleanup(func() { _ = docker.New("").Quiet("", "volume", "rm", "-f", v) })
			}
			s, _ := newService(t, repo)
			if _, err := s.Generate(base, false); err != nil {
				t.Fatal(err)
			}
			itest.WriteFile(t, repo.Root, paths.PiholeList, "192.168.1.2 nas.lan\n")
			if err := envfile.Set(repo.Path("pihole/.env"), "MY_NOTE", "keep"); err != nil {
				t.Fatal(err)
			}
			before := map[string]map[string]string{}
			for _, f := range paths.EnvFiles {
				before[f] = env(t, repo, f)
			}
			res, err := s.Generate(base, true)
			if err != nil {
				t.Fatal(err)
			}
			check := func(list []string, rotated bool) {
				for _, k := range list {
					file, key, _ := strings.Cut(k, ":")
					if got := env(t, repo, file)[key] != before[file][key]; got != rotated {
						t.Errorf("%s rotated = %v, want %v", k, got, rotated)
					}
				}
			}
			check(tc.rotated, true)
			check(tc.kept, false)
			for _, w := range tc.wantKeptMsg {
				if !slices.ContainsFunc(res.Kept, func(k string) bool { return strings.HasPrefix(k, w) }) {
					t.Errorf("Kept %v lacks %s", res.Kept, w)
				}
			}
			if itest.ReadFile(repo.Path(paths.PiholeList)) != "192.168.1.2 nas.lan\n" {
				t.Error("custom.list was rewritten")
			}
		})
	}
}

// TestMigrateSharedNetworkEnv drops only the settings the move onto the shared network made
// obsolete — a deliberate container-name override stays.
func TestMigrateSharedNetworkEnv(t *testing.T) {
	repo := itest.Repo(t)
	itest.WriteFile(t, repo.Root, "caddy/.env", "SERVER_IP=1.2.3.4\nVAULT_UPSTREAM=host.docker.internal:8082\n"+
		"CLOUD_UPSTREAM=nextcloud-app:80\nHOME_UPSTREAM=host.docker.internal:8088\n")
	itest.WriteFile(t, repo.Root, "vaultwarden/.env", "VW_HTTP_PORT=8082\nVW_DOMAIN=x\n")
	s, out := newService(t, repo)
	s.MigrateSharedNetworkEnv()
	cases := []struct {
		file, key string
		kept      bool
	}{
		{"caddy/.env", "VAULT_UPSTREAM", false},
		{"caddy/.env", "CLOUD_UPSTREAM", true},
		{"caddy/.env", "HOME_UPSTREAM", true},
		{"vaultwarden/.env", "VW_HTTP_PORT", false},
		{"vaultwarden/.env", "VW_DOMAIN", true},
	}
	for _, tc := range cases {
		if _, ok := env(t, repo, tc.file)[tc.key]; ok != tc.kept {
			t.Errorf("%s %s kept=%v, want %v", tc.file, tc.key, ok, tc.kept)
		}
	}
	if !strings.Contains(out.String(), "migrated caddy/.env") {
		t.Errorf("the migration should say what it did: %q", out.String())
	}
	out.Reset()
	s.MigrateSharedNetworkEnv()
	if out.Len() != 0 {
		t.Errorf("a second run should be silent: %q", out.String())
	}
}

// TestMissingEnv: `up` only needs the .env of apps that are switched on, plus Caddy's.
func TestMissingEnv(t *testing.T) {
	cases := []struct {
		name       string
		present    []string
		disabled   []string
		want       []string
		configured bool
	}{
		{"nothing set up", nil, nil, []string{"vaultwarden", "nextcloud", "pihole", "caddy"}, false},
		{"all present", []string{"vaultwarden", "nextcloud", "pihole", "caddy"}, nil, nil, true},
		{"Pi-hole off and never configured", []string{"vaultwarden", "nextcloud", "caddy"}, []string{"pihole"}, nil, false},
		{"Caddy missing", []string{"vaultwarden", "nextcloud", "pihole"}, nil, []string{"caddy"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			for _, d := range tc.present {
				itest.WriteFile(t, repo.Root, d+"/.env", "X=1\n")
			}
			s, _ := newService(t, repo)
			if got := s.MissingEnv(config.Settings{DisabledApps: tc.disabled}); !slices.Equal(got, tc.want) {
				t.Errorf("MissingEnv = %v, want %v", got, tc.want)
			}
			if s.Configured() != tc.configured {
				t.Errorf("Configured = %v, want %v", s.Configured(), tc.configured)
			}
		})
	}
}

// TestNextcloudTrust adds a new server address to a real, installed Nextcloud's trusted domains
// through occ — once — and says so when Nextcloud isn't running.
func TestNextcloudTrust(t *testing.T) {
	itest.NeedDocker(t)
	repo := itest.Repo(t)
	s, out := newService(t, repo)
	if added, err := s.EnsureNextcloudTrusts("10.0.0.9"); err == nil || added {
		t.Fatalf("without Nextcloud: added=%v err=%v", added, err)
	}
	s.TrustNextcloudIP("10.0.0.9")
	if !strings.Contains(out.String(), "Nextcloud isn't running") {
		t.Errorf("TrustNextcloudIP without Nextcloud: %q", out.String())
	}

	image := itest.Image(t, "nextcloud", "nextcloud:")
	itest.Pull(t, image)
	t.Cleanup(func() { _ = docker.New("").Quiet("", "rm", "-f", "-v", "nextcloud-app") })
	itest.Docker(t, "run", "-d", "--name", "nextcloud-app", "-e", "SQLITE_DATABASE=nextcloud",
		"-e", "NEXTCLOUD_ADMIN_USER=admin", "-e", "NEXTCLOUD_ADMIN_PASSWORD=itest-admin-pw-123",
		"-e", "NEXTCLOUD_TRUSTED_DOMAINS=10.0.0.5", image)
	trusted := func() string {
		o, _ := docker.New("").Combined("", "exec", "-u", "www-data", "nextcloud-app", "php", "occ", "config:system:get", "trusted_domains")
		return o
	}
	// Installed, and the image's entrypoint has applied NEXTCLOUD_TRUSTED_DOMAINS (it does that just
	// after `occ status` starts saying installed).
	if !docker.Wait(3*time.Minute, func() bool { return slices.Contains(strings.Fields(trusted()), "10.0.0.5") }) {
		t.Fatal("Nextcloud didn't finish installing")
	}
	cases := []struct {
		ip        string
		wantAdded bool
	}{
		{"10.0.0.5", false}, // already trusted at install
		{"10.0.0.9", true},
		{"10.0.0.9", false}, // only once
	}
	for _, tc := range cases {
		added, err := s.EnsureNextcloudTrusts(tc.ip)
		if err != nil || added != tc.wantAdded {
			t.Errorf("EnsureNextcloudTrusts(%s) = %v, %v; want added=%v", tc.ip, added, err, tc.wantAdded)
		}
	}
	out.Reset()
	s.TrustNextcloudIP("10.0.0.10")
	got := strings.Fields(trusted())
	if !strings.Contains(out.String(), "trusted_domains += 10.0.0.10") || !slices.Equal(got, []string{"localhost", "10.0.0.5", "10.0.0.9", "10.0.0.10"}) {
		t.Errorf("TrustNextcloudIP: %q; trusted_domains now %q", out.String(), got)
	}
}
