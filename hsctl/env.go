package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Secret is a human login surfaced after generation (to save into Vaultwarden).
type Secret struct{ Label, Value string }

// generated is what Generate did: the human logins it created or rotated, the settings it changed
// in .env files that already existed, and (with force) the secrets it deliberately kept.
type generated struct {
	Secrets []Secret
	Changed []string
	Kept    []string
}

// Generate writes every service's .env (with fresh secrets), pihole/custom.list, and a
// personalized WELCOME.txt. An existing .env gets the current settings applied (see
// applySettings) — so changing a setting with `hsctl setup` or the wizard actually reaches the
// stack — and keeps its secrets unless force is set.
//
// force rotates the secrets IN PLACE (other lines, comments and hand edits survive), except the
// ones live data depends on: Postgres only takes POSTGRES_PASSWORD when it first creates its
// database, and Nextcloud only reads NC_ADMIN_* when it first installs. Rotating those while that
// data exists breaks Nextcloud (or prints a login that doesn't work), so they're kept — and
// reported — until the data is gone (`hsctl down --volumes`).
func (c Config) Generate(repo string, force bool) (generated, error) {
	var res generated
	changed, err := c.applySettings(repo)
	if err != nil {
		return res, err
	}
	res.Changed = changed
	add := func(label, value string) { res.Secrets = append(res.Secrets, Secret{label, value}) }
	// create writes rel when it doesn't exist yet; reports whether it did.
	create := func(rel, content string) (bool, error) {
		path := filepath.Join(repo, rel)
		if fileExists(path) {
			return false, nil
		}
		return true, writeFile0600(path, content)
	}
	// rotate replaces one secret in an existing file (force only).
	rotate := func(rel, key, value string) error {
		return setEnvKey(filepath.Join(repo, rel), key, value)
	}

	// vaultwarden
	// Store the /admin token HASHED (Argon2id), never plaintext. The raw token is surfaced
	// once below for the user to save; '$' is doubled so docker-compose passes it through.
	vwToken := genPassword(40)
	vwStored := escapeDollarsForCompose(argon2idPHC(vwToken))
	const vwLabel = "Vaultwarden /admin token (SAVE — not recoverable):"
	if wrote, err := create("vaultwarden/.env", fmt.Sprintf(
		"VW_DOMAIN=https://%s:8443\nVW_ADMIN_TOKEN=%s\nVW_SIGNUPS_ALLOWED=%s\n",
		c.ServerIP, vwStored, boolStr(c.VWSignupsAllowed, "true", "false"))); err != nil {
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
	ncPw := genPassword(20)
	const ncLabel = "Nextcloud (user 'admin'):"
	if wrote, err := create("nextcloud/.env", fmt.Sprintf(
		"POSTGRES_DB=nextcloud\nPOSTGRES_USER=nextcloud\nPOSTGRES_PASSWORD=%s\nREDIS_PASSWORD=%s\n"+
			"NC_ADMIN_USER=admin\nNC_ADMIN_PASSWORD=%s\nNC_TRUSTED_DOMAINS=%s\n"+
			"NC_TRUSTED_PROXIES=172.16.0.0/12\n",
		genPassword(32), genPassword(32), ncPw, c.ServerIP)); err != nil {
		return res, err
	} else if wrote {
		add(ncLabel, ncPw)
	} else if force {
		// Redis keeps no password of its own (it's passed on the command line), so it always rotates.
		if err := rotate("nextcloud/.env", "REDIS_PASSWORD", genPassword(32)); err != nil {
			return res, err
		}
		if volumeExistsOrUnknown(repo, "nextcloud_db-data-v18") || volumeExistsOrUnknown(repo, "nextcloud_db-data") {
			res.Kept = append(res.Kept, "POSTGRES_PASSWORD — the existing Nextcloud database was created with it")
		} else if err := rotate("nextcloud/.env", "POSTGRES_PASSWORD", genPassword(32)); err != nil {
			return res, err
		}
		if volumeExistsOrUnknown(repo, "nextcloud_nc-html") {
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
	phPw := genPassword(20)
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
	listPath := filepath.Join(repo, "pihole/custom.list")
	if !fileExists(listPath) {
		if err := writeFile0644(listPath,
			"# Pi-hole local DNS records (none — apps are reached by IP:port).\n"); err != nil {
			return res, err
		}
	}

	// caddy — HTTPS per app at the server IP + port (cert SAN = the IP). No secrets, so force
	// leaves an existing one alone (its settings were applied above).
	if _, err := create("caddy/.env", c.caddyEnv()); err != nil {
		return res, err
	}

	// personalized handout
	welcome := fmt.Sprintf(`Homeserver — quick reference
  Dashboard (home page):          https://%[1]s
  Install the certificate first:  http://%[1]s/        (download root.crt)
  Passwords (Vaultwarden):        https://%[1]s:8443
  Files (Nextcloud):              https://%[1]s:8444
  Pi-hole admin:                  https://%[1]s:8445/admin
  PDF tools / Utilities / Image:  https://%[1]s:8446  /  :8447  /  :8448
Step-by-step per device: see ONBOARDING.md
`, c.ServerIP)
	if err := writeFile0644(filepath.Join(repo, "WELCOME.txt"), welcome); err != nil {
		return res, err
	}

	return res, nil
}

// volumeExistsOrUnknown reports whether a docker volume exists — and says yes when docker can't
// be asked, because the caller uses it to decide whether rotating a secret would strand data.
func volumeExistsOrUnknown(repo, name string) bool {
	if err := dockerCmd(repo, "volume", "inspect", name).Run(); err == nil {
		return true
	}
	if err := dockerCmd(repo, "info").Run(); err != nil {
		return true // docker unreachable: assume the data is there
	}
	return false
}

// applySettings writes the non-secret settings in c into the .env files that already exist, and
// returns a line per value it changed. Secrets are never touched. Values hsctl doesn't own are left
// alone: VW_DOMAIN and NC_TRUSTED_DOMAINS only have the old server IP swapped for the new one (so a
// hand-set hostname survives), and HOME_UPSTREAM only follows the dashboard port while it still
// points at the host gateway. Containers pick the changes up on the next `hsctl up`.
func (c Config) applySettings(repo string) ([]string, error) {
	var changed []string
	set := func(rel, key, val string) error {
		path := filepath.Join(repo, rel)
		kv, err := readKV(path)
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
		if err := setEnvKey(path, key, val); err != nil {
			return err
		}
		if !ok {
			cur = "(unset)"
		}
		changed = append(changed, fmt.Sprintf("%s: %s %s -> %s", rel, key, cur, val))
		return nil
	}

	caddy, _ := readKV(filepath.Join(repo, "caddy/.env"))
	oldIP := caddy["SERVER_IP"]
	vaultPort := caddy["VAULT_HTTPS"]
	if vaultPort == "" {
		vaultPort = "8443"
	}
	type step struct{ rel, key, val string }
	steps := []step{
		{"caddy/.env", "SERVER_IP", c.ServerIP},
		{"caddy/.env", "ACME_EMAIL", c.ACMEEmail},
		{"vaultwarden/.env", "VW_SIGNUPS_ALLOWED", boolStr(c.VWSignupsAllowed, "true", "false")},
		{"pihole/.env", "TZ", c.TZ},
		{"pihole/.env", "PIHOLE_DNS_BIND", c.PiholeDNSBind},
	}
	if up := caddy["HOME_UPSTREAM"]; up == "" || strings.HasPrefix(up, "host.docker.internal:") {
		steps = append(steps, step{"caddy/.env", "HOME_UPSTREAM", fmt.Sprintf("host.docker.internal:%d", c.UIPort)})
	}
	if oldIP != "" && oldIP != c.ServerIP {
		if kv, err := readKV(filepath.Join(repo, "vaultwarden/.env")); err == nil {
			if d := kv["VW_DOMAIN"]; d == "https://"+oldIP+":"+vaultPort || d == "" {
				steps = append(steps, step{"vaultwarden/.env", "VW_DOMAIN", "https://" + c.ServerIP + ":" + vaultPort})
			}
		}
		if kv, err := readKV(filepath.Join(repo, "nextcloud/.env")); err == nil {
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

// caddyEnv is the content of caddy/.env for this config. It holds no secrets — just the
// server IP (cert SAN), the ACME contact and where the host-side dashboard listens.
func (c Config) caddyEnv() string {
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

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
