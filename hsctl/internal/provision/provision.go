// Package provision writes the stack's configuration: each service's .env with fresh secrets,
// pihole/custom.list and the WELCOME.txt handout — and later applies changed settings to those
// files, rotates the secrets that are safe to rotate, and keeps old installs in step with changes
// to the stack's layout.
package provision

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secret"
)

// Docker is what provisioning asks Docker: whether data a secret protects already exists, and
// Nextcloud's own idea of its trusted domains.
type Docker interface {
	VolumeExistsOrUnknown(name string) bool
	ContainerState(name string) string
	Combined(dir string, args ...string) (string, error)
}

// Service provisions one repo.
type Service struct {
	Repo   paths.Repo
	Docker Docker
	Out    io.Writer // progress and notes
	Err    io.Writer // warnings
}

// New returns the provisioning service for repo.
func New(repo paths.Repo, dk Docker) *Service {
	return &Service{Repo: repo, Docker: dk, Out: os.Stdout, Err: os.Stderr}
}

// Secret is a human login surfaced after generation (to save into Vaultwarden).
type Secret struct{ Label, Value string }

// Result is what Generate did: the human logins it created or rotated, the settings it changed in
// .env files that already existed, and (with force) the secrets it deliberately kept.
type Result struct {
	Secrets []Secret
	Changed []string
	Kept    []string
}

// Generate writes every service's .env (with fresh secrets), pihole/custom.list, and a
// personalized WELCOME.txt. An existing .env gets the current settings applied (see Apply) — so
// changing a setting with `hsctl setup` or the wizard actually reaches the stack — and keeps its
// secrets unless force is set.
//
// force rotates the secrets IN PLACE (other lines, comments and hand edits survive), except the
// ones live data depends on: Postgres only takes POSTGRES_PASSWORD when it first creates its
// database, and Nextcloud only reads NC_ADMIN_* when it first installs. Rotating those while that
// data exists breaks Nextcloud (or prints a login that doesn't work), so they're kept — and
// reported — until the data is gone (`hsctl down --volumes`).
func (s *Service) Generate(c config.Settings, force bool) (Result, error) {
	var res Result
	changed, err := s.Apply(c)
	if err != nil {
		return res, err
	}
	res.Changed = changed
	add := func(label, value string) { res.Secrets = append(res.Secrets, Secret{label, value}) }
	// create writes rel when it doesn't exist yet; reports whether it did.
	create := func(rel, content string) (bool, error) {
		path := s.Repo.Path(rel)
		if fsutil.Exists(path) {
			return false, nil
		}
		return true, fsutil.WritePrivate(path, content)
	}
	// rotate replaces one secret in an existing file (force only).
	rotate := func(rel, key, value string) error {
		return envfile.Set(s.Repo.Path(rel), key, value)
	}

	// vaultwarden
	// Store the /admin token HASHED (Argon2id), never plaintext. The raw token is surfaced once
	// below for the user to save; '$' is doubled so docker compose passes it through.
	vwToken := secret.Password(40)
	vwStored := secret.EscapeDollars(secret.Argon2idPHC(vwToken))
	const vwLabel = "Vaultwarden /admin token (SAVE — not recoverable):"
	if wrote, err := create("vaultwarden/.env", fmt.Sprintf(
		"VW_DOMAIN=https://%s:8443\nVW_ADMIN_TOKEN=%s\nVW_SIGNUPS_ALLOWED=%s\n",
		c.ServerIP, vwStored, config.BoolStr(c.VWSignupsAllowed, "true", "false"))); err != nil {
		return res, err
	} else if wrote {
		add(vwLabel, vwToken)
	} else if force {
		if err := rotate("vaultwarden/.env", "VW_ADMIN_TOKEN", vwStored); err != nil {
			return res, err
		}
		add(vwLabel, vwToken)
	}

	// nextcloud
	ncPw := secret.Password(20)
	const ncLabel = "Nextcloud (user 'admin'):"
	if wrote, err := create("nextcloud/.env", fmt.Sprintf(
		"POSTGRES_DB=nextcloud\nPOSTGRES_USER=nextcloud\nPOSTGRES_PASSWORD=%s\nREDIS_PASSWORD=%s\n"+
			"NC_ADMIN_USER=admin\nNC_ADMIN_PASSWORD=%s\nNC_TRUSTED_DOMAINS=%s\n"+
			"NC_TRUSTED_PROXIES=172.16.0.0/12\n",
		secret.Password(32), secret.Password(32), ncPw, c.ServerIP)); err != nil {
		return res, err
	} else if wrote {
		add(ncLabel, ncPw)
	} else if force {
		// Redis keeps no password of its own (it's passed on the command line), so it always rotates.
		if err := rotate("nextcloud/.env", "REDIS_PASSWORD", secret.Password(32)); err != nil {
			return res, err
		}
		dbData := false
		for _, v := range layout.NextcloudDBVolumes {
			dbData = dbData || s.Docker.VolumeExistsOrUnknown(v)
		}
		if dbData {
			res.Kept = append(res.Kept, "POSTGRES_PASSWORD — the existing Nextcloud database was created with it")
		} else if err := rotate("nextcloud/.env", "POSTGRES_PASSWORD", secret.Password(32)); err != nil {
			return res, err
		}
		if s.Docker.VolumeExistsOrUnknown("nextcloud_nc-html") {
			res.Kept = append(res.Kept, "NC_ADMIN_PASSWORD — Nextcloud is already installed and only reads it on first install "+
				"(change the admin password inside Nextcloud instead)")
		} else {
			if err := rotate("nextcloud/.env", "NC_ADMIN_PASSWORD", ncPw); err != nil {
				return res, err
			}
			add(ncLabel, ncPw)
		}
	}

	// pihole — its admin password is applied from the env on every start, so it always rotates.
	phPw := secret.Password(20)
	const phLabel = "Pi-hole admin:"
	if wrote, err := create("pihole/.env", fmt.Sprintf(
		"TZ=%s\nPIHOLE_PASSWORD=%s\nPIHOLE_UPSTREAMS=1.1.1.1;9.9.9.9\nPIHOLE_DNS_BIND=%s\n",
		c.TZ, phPw, c.PiholeDNSBind)); err != nil {
		return res, err
	} else if wrote {
		add(phLabel, phPw)
	} else if force {
		if err := rotate("pihole/.env", "PIHOLE_PASSWORD", phPw); err != nil {
			return res, err
		}
		add(phLabel, phPw)
	}

	// pihole/custom.list — no local hostnames (apps are reached by IP:port). Kept as an
	// almost-empty file so the bind-mount has a file to mount. Never overwritten: any records in it
	// are the admin's.
	if listPath := s.Repo.Path(paths.PiholeList); !fsutil.Exists(listPath) {
		if err := fsutil.WritePublic(listPath,
			"# Pi-hole local DNS records (none — apps are reached by IP:port).\n"); err != nil {
			return res, err
		}
	}

	// caddy — HTTPS per app at the server IP + port (cert SAN = the IP). No secrets, so force
	// leaves an existing one alone (its settings were applied above).
	if _, err := create("caddy/.env", CaddyEnv(c)); err != nil {
		return res, err
	}

	// personalized handout
	welcome := fmt.Sprintf(`Homeserver — quick reference
  Dashboard (home page):          https://%[1]s
  Install the certificate first:  http://%[1]s/        (download homeserver-ca.crt)
  Passwords (Vaultwarden):        https://%[1]s:8443
  Files (Nextcloud):              https://%[1]s:8444
  Pi-hole admin:                  https://%[1]s:8445/admin
  PDF tools / Utilities / Image:  https://%[1]s:8446  /  :8447  /  :8448
Step-by-step per device: see ONBOARDING.md
`, c.ServerIP)
	if err := fsutil.WritePublic(s.Repo.Path(paths.Welcome), welcome); err != nil {
		return res, err
	}
	return res, nil
}

// Apply writes the non-secret settings in c into the .env files that already exist, and returns a
// line per value it changed. Secrets are never touched. Values hsctl doesn't own are left alone:
// VW_DOMAIN and NC_TRUSTED_DOMAINS only have the old server IP swapped for the new one (so a
// hand-set hostname survives), and HOME_UPSTREAM only follows the dashboard port while it still
// points at the host gateway. Containers pick the changes up on the next `hsctl up`.
func (s *Service) Apply(c config.Settings) ([]string, error) {
	var changed []string
	set := func(rel, key, val string) error {
		path := s.Repo.Path(rel)
		kv, err := envfile.Read(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // not generated yet — Generate creates it
			}
			return err
		}
		cur, ok := kv[key]
		if ok && cur == val {
			return nil
		}
		if err := envfile.Set(path, key, val); err != nil {
			return err
		}
		if !ok {
			cur = "(unset)"
		}
		changed = append(changed, fmt.Sprintf("%s: %s %s -> %s", rel, key, cur, val))
		return nil
	}

	caddy, _ := envfile.Read(s.Repo.Path("caddy/.env"))
	oldIP := caddy["SERVER_IP"]
	vaultPort := caddy["VAULT_HTTPS"]
	if vaultPort == "" {
		vaultPort = "8443"
	}
	type step struct{ rel, key, val string }
	steps := []step{
		{"caddy/.env", "SERVER_IP", c.ServerIP},
		{"caddy/.env", "ACME_EMAIL", c.ACMEEmail},
		{"vaultwarden/.env", "VW_SIGNUPS_ALLOWED", config.BoolStr(c.VWSignupsAllowed, "true", "false")},
		{"pihole/.env", "TZ", c.TZ},
		{"pihole/.env", "PIHOLE_DNS_BIND", c.PiholeDNSBind},
	}
	if up := caddy["HOME_UPSTREAM"]; up == "" || strings.HasPrefix(up, "host.docker.internal:") {
		steps = append(steps, step{"caddy/.env", "HOME_UPSTREAM", fmt.Sprintf("host.docker.internal:%d", c.UIPort)})
	}
	if oldIP != "" && oldIP != c.ServerIP {
		if kv, err := envfile.Read(s.Repo.Path("vaultwarden/.env")); err == nil {
			if d := kv["VW_DOMAIN"]; d == "https://"+oldIP+":"+vaultPort || d == "" {
				steps = append(steps, step{"vaultwarden/.env", "VW_DOMAIN", "https://" + c.ServerIP + ":" + vaultPort})
			}
		}
		if kv, err := envfile.Read(s.Repo.Path("nextcloud/.env")); err == nil {
			steps = append(steps, step{"nextcloud/.env", "NC_TRUSTED_DOMAINS",
				swapField(kv["NC_TRUSTED_DOMAINS"], oldIP, c.ServerIP)})
		}
	}
	for _, st := range steps {
		if err := set(st.rel, st.key, st.val); err != nil {
			return changed, fmt.Errorf("update %s: %w", st.rel, err)
		}
	}
	return changed, nil
}

// swapField replaces old with new in a space-separated list (appending new if old isn't there).
func swapField(list, old, new string) string {
	f := strings.Fields(list)
	for i, v := range f {
		if v == old {
			f[i] = new
			return strings.Join(f, " ")
		}
	}
	if !slices.Contains(f, new) {
		f = append(f, new)
	}
	return strings.Join(f, " ")
}

// CaddyEnv is the content of caddy/.env for c. It holds no secrets — just the server IP (cert
// SAN), the ACME contact and where the host-side dashboard listens.
func CaddyEnv(c config.Settings) string {
	return fmt.Sprintf(
		"SERVER_IP=%s\nACME_EMAIL=%s\n"+
			"# App upstreams are the container names on the shared \"edge\" network — set as\n"+
			"# defaults in caddy/docker-compose.yml, so they're intentionally NOT listed here.\n"+
			"# The dashboard runs on the host, so Caddy reaches it via the docker host gateway:\n"+
			"HOME_UPSTREAM=host.docker.internal:%d\n"+
			"VAULT_HTTPS=8443\nCLOUD_HTTPS=8444\nPIHOLE_HTTPS=8445\nHOME_HTTPS=443\n"+
			"STIRLING_HTTPS=8446\nITTOOLS_HTTPS=8447\nIMAGETOOLS_HTTPS=8448\n",
		c.ServerIP, c.ACMEEmail, c.UIPort)
}

// MigrateSharedNetworkEnv strips settings made obsolete by the move onto the shared edge network,
// so an existing install picks up the new behaviour without a --force re-setup: the app upstreams
// Caddy used to reach via host-published ports (now container-name defaults in
// caddy/docker-compose.yml) and the per-app host ports the apps no longer publish. A stale
// caddy/.env would otherwise still point Caddy at now-dead host ports. Best-effort + idempotent.
func (s *Service) MigrateSharedNetworkEnv() {
	warn := func(rel string, err error) {
		fmt.Fprintf(s.Err, "warning: could not migrate %s to the shared network: %v\n", rel, err)
	}
	note := func(rel string) {
		fmt.Fprintf(s.Out, "migrated %s onto the shared Caddy network (dropped obsolete host-port settings)\n", rel)
	}

	// caddy/.env: drop only the STALE host-gateway upstreams (host.docker.internal:<port>). A
	// user's deliberate container-name override (which caddy/.env.example explicitly permits, for
	// a renamed service) doesn't contain host.docker.internal, so it's left intact.
	upstream := map[string]bool{
		"VAULT_UPSTREAM": true, "CLOUD_UPSTREAM": true, "PIHOLE_UPSTREAM": true,
		"STIRLING_UPSTREAM": true, "ITTOOLS_UPSTREAM": true, "IMAGETOOLS_UPSTREAM": true,
	}
	if changed, err := envfile.RemoveMatching(s.Repo.Path("caddy/.env"), func(k, v string) bool {
		return upstream[k] && strings.Contains(v, "host.docker.internal")
	}); err != nil {
		warn("caddy/.env", err)
	} else if changed {
		note("caddy/.env")
	}

	// The per-app host ports are simply gone now (the apps aren't published), so drop them outright.
	for _, m := range []struct{ rel, key string }{
		{"vaultwarden/.env", "VW_HTTP_PORT"},
		{"nextcloud/.env", "NC_HTTP_PORT"},
		{"pihole/.env", "PIHOLE_WEB_PORT"},
	} {
		if changed, err := envfile.RemoveKeys(s.Repo.Path(m.rel), m.key); err != nil {
			warn(m.rel, err)
		} else if changed {
			note(m.rel)
		}
	}
}

// TrustNextcloudIP adds a new server IP to a running Nextcloud's own trusted_domains: Nextcloud only
// reads NC_TRUSTED_DOMAINS on its first install, so without this an IP change locks it behind
// "access through untrusted domain".
func (s *Service) TrustNextcloudIP(ip string) {
	if s.Docker.ContainerState(layout.NextcloudApp) != "running" {
		fmt.Fprintln(s.Out, "  (Nextcloud isn't running — `hsctl up` adds the new IP to its trusted domains.)")
		return
	}
	if added, err := s.EnsureNextcloudTrusts(ip); err != nil {
		fmt.Fprintf(s.Err, "  could not add %s to Nextcloud's trusted domains yet (%v) — `hsctl up` retries it\n", ip, err)
	} else if added {
		fmt.Fprintf(s.Out, "  nextcloud: trusted_domains += %s\n", ip)
	}
}

// EnsureNextcloudTrusts makes sure ip is in Nextcloud's trusted_domains (via occ), reporting whether
// it had to add it. It errors while Nextcloud isn't installed/ready yet.
func (s *Service) EnsureNextcloudTrusts(ip string) (bool, error) {
	occ := func(args ...string) (string, error) {
		return s.Docker.Combined(s.Repo.Root, append([]string{"exec", "-u", "www-data", layout.NextcloudApp, "php", "occ"}, args...)...)
	}
	out, err := occ("config:system:get", "trusted_domains")
	if err != nil {
		return false, fmt.Errorf("occ: %s", strings.TrimSpace(out))
	}
	domains := strings.Fields(out)
	for _, d := range domains {
		if d == ip {
			return false, nil
		}
	}
	if out, err := occ("config:system:set", "trusted_domains", strconv.Itoa(len(domains)), "--value="+ip); err != nil {
		return false, fmt.Errorf("occ: %s", strings.TrimSpace(out))
	}
	return true, nil
}

// MissingEnv lists what `up` needs but setup hasn't generated: the .env of every ENABLED core app,
// plus Caddy's (without it Caddy would issue its certificate for a blank address). A switched off
// app — Pi-hole, say — doesn't need its .env to exist.
func (s *Service) MissingEnv(c config.Settings) []string {
	var miss []string
	for _, svc := range append(append([]string{}, layout.Core...), layout.Proxy) {
		if c.IsDisabled(svc) {
			continue
		}
		if !fsutil.Exists(s.Repo.Path(svc, ".env")) {
			miss = append(miss, svc)
		}
	}
	return miss
}

// Configured reports whether setup has run: every core app has its .env.
func (s *Service) Configured() bool {
	for _, svc := range layout.Core {
		if !fsutil.Exists(s.Repo.Path(svc, ".env")) {
			return false
		}
	}
	return true
}
