package provision

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

func newRepo(t *testing.T) paths.Repo {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"vaultwarden", "nextcloud", "pihole", "caddy"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return paths.Repo{Root: root}
}

var base = config.Settings{ServerIP: "10.0.0.5", TZ: "Etc/UTC", ACMEEmail: "a@b", UIPort: 8088,
	PiholeDNSBind: "0.0.0.0", VWSignupsAllowed: true}

// TestGenerateAppliesSettings: re-running setup with new settings must reach the existing .env
// files (non-secret keys only), and leave every secret as it was.
func TestGenerateAppliesSettings(t *testing.T) {
	cases := []struct {
		name   string
		change func(*config.Settings)
		want   map[string]string // "file:KEY" -> value
	}{
		{"signups off", func(c *config.Settings) { c.VWSignupsAllowed = false },
			map[string]string{"vaultwarden/.env:VW_SIGNUPS_ALLOWED": "false"}},
		{"timezone", func(c *config.Settings) { c.TZ = "Europe/Berlin" },
			map[string]string{"pihole/.env:TZ": "Europe/Berlin"}},
		{"dashboard port", func(c *config.Settings) { c.UIPort = 9000 },
			map[string]string{"caddy/.env:HOME_UPSTREAM": "host.docker.internal:9000"}},
		{"new server IP", func(c *config.Settings) { c.ServerIP = "10.0.0.9" },
			map[string]string{
				"caddy/.env:SERVER_IP":              "10.0.0.9",
				"vaultwarden/.env:VW_DOMAIN":        "https://10.0.0.9:8443",
				"nextcloud/.env:NC_TRUSTED_DOMAINS": "10.0.0.9",
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			s := New(repo, docker.New(repo.Root))
			c := base
			if _, err := s.Generate(c, false); err != nil {
				t.Fatal(err)
			}
			before, _ := envfile.Read(repo.Path("nextcloud/.env"))
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
				kv, _ := envfile.Read(repo.Path(file))
				if kv[key] != want {
					t.Errorf("%s %s = %q, want %q", file, key, kv[key], want)
				}
			}
			after, _ := envfile.Read(repo.Path("nextcloud/.env"))
			if after["POSTGRES_PASSWORD"] != before["POSTGRES_PASSWORD"] {
				t.Error("a secret changed")
			}
		})
	}
}

// TestMigrateSharedNetworkEnv drops only the obsolete host-port settings.
func TestMigrateSharedNetworkEnv(t *testing.T) {
	repo := newRepo(t)
	files := map[string]string{
		"caddy/.env":       "SERVER_IP=1.2.3.4\nVAULT_UPSTREAM=host.docker.internal:8082\nCLOUD_UPSTREAM=nextcloud-app:80\nHOME_UPSTREAM=host.docker.internal:8088\n",
		"vaultwarden/.env": "VW_HTTP_PORT=8082\nVW_DOMAIN=x\n",
	}
	for f, c := range files {
		if err := os.WriteFile(repo.Path(f), []byte(c), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(repo, docker.New(repo.Root))
	s.Out, s.Err = os.Stderr, os.Stderr
	s.MigrateSharedNetworkEnv()
	cases := []struct {
		file, key string
		kept      bool
	}{
		{"caddy/.env", "VAULT_UPSTREAM", false},
		{"caddy/.env", "CLOUD_UPSTREAM", true}, // a deliberate container-name override stays
		{"caddy/.env", "HOME_UPSTREAM", true},
		{"vaultwarden/.env", "VW_HTTP_PORT", false},
		{"vaultwarden/.env", "VW_DOMAIN", true},
	}
	for _, tc := range cases {
		kv, _ := envfile.Read(repo.Path(tc.file))
		if _, ok := kv[tc.key]; ok != tc.kept {
			t.Errorf("%s %s kept=%v, want %v", tc.file, tc.key, ok, tc.kept)
		}
	}
}

// TestMissingEnv: `up` only needs the .env of apps that are switched on, plus Caddy's.
func TestMissingEnv(t *testing.T) {
	cases := []struct {
		name     string
		present  []string
		disabled []string
		want     []string
	}{
		{"nothing set up", nil, nil, []string{"vaultwarden", "nextcloud", "pihole", "caddy"}},
		{"all present", []string{"vaultwarden", "nextcloud", "pihole", "caddy"}, nil, nil},
		{"pihole off and never configured", []string{"vaultwarden", "nextcloud", "caddy"}, []string{"pihole"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			for _, d := range tc.present {
				if err := os.WriteFile(repo.Path(d, ".env"), []byte("X=1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := New(repo, nil).MissingEnv(config.Settings{DisabledApps: tc.disabled}); !slices.Equal(got, tc.want) {
				t.Errorf("missing = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGenerateForceRotatesInPlace: --force rotates the secrets that are safe to rotate, in place —
// the admin's own .env additions and Pi-hole's local DNS list survive, and the new logins are
// reported.
func TestGenerateForceRotatesInPlace(t *testing.T) {
	repo := newRepo(t)
	svc := New(repo, docker.New(repo.Root))
	if _, err := svc.Generate(base, false); err != nil {
		t.Fatal(err)
	}
	list := repo.Path(paths.PiholeList)
	if err := os.WriteFile(list, []byte("192.168.1.2 nas.lan\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := envfile.Set(repo.Path("pihole/.env"), "MY_NOTE", "keep"); err != nil {
		t.Fatal(err)
	}
	before := map[string]map[string]string{}
	for _, f := range []string{"vaultwarden/.env", "pihole/.env", "caddy/.env"} {
		before[f], _ = envfile.Read(repo.Path(f))
	}
	res, err := svc.Generate(base, true)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		file, key string
		rotated   bool
	}{
		{"vaultwarden/.env", "VW_ADMIN_TOKEN", true},
		{"pihole/.env", "PIHOLE_PASSWORD", true},
		{"pihole/.env", "MY_NOTE", false},
		{"caddy/.env", "SERVER_IP", false},
	}
	for _, tc := range cases {
		t.Run(tc.file+" "+tc.key, func(t *testing.T) {
			after, _ := envfile.Read(repo.Path(tc.file))
			if got := after[tc.key] != before[tc.file][tc.key]; got != tc.rotated {
				t.Errorf("rotated = %v, want %v", got, tc.rotated)
			}
		})
	}
	if b, _ := os.ReadFile(list); string(b) != "192.168.1.2 nas.lan\n" {
		t.Errorf("custom.list was rewritten: %q", b)
	}
	if len(res.Secrets) < 2 {
		t.Errorf("rotated logins not reported: %v", res.Secrets)
	}
}
