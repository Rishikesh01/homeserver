package main

import (
	"fmt"
	"path/filepath"
)

// Secret is a human login surfaced after generation (to save into Vaultwarden).
type Secret struct{ Label, Value string }

// Generate writes every service's .env (with fresh secrets), pihole/custom.list, and
// a personalized WELCOME.txt. Existing .env files are left untouched unless force is
// set. It returns the human logins for any .env it newly created.
func (c Config) Generate(repo string, force bool) ([]Secret, error) {
	var secrets []Secret
	// writeEnv writes path only if absent (or force); reports whether it wrote.
	writeEnv := func(rel, content string) (bool, error) {
		path := filepath.Join(repo, rel)
		if fileExists(path) && !force {
			return false, nil
		}
		return true, writeFile0600(path, content)
	}

	// vaultwarden
	// Store the /admin token HASHED (Argon2id), never plaintext. The raw token is surfaced
	// once below for the user to save; '$' is doubled so docker-compose passes it through.
	vwToken := genPassword(40)
	vwStored := escapeDollarsForCompose(argon2idPHC(vwToken))
	if wrote, err := writeEnv("vaultwarden/.env", fmt.Sprintf(
		"VW_DOMAIN=https://%s:8443\nVW_ADMIN_TOKEN=%s\nVW_SIGNUPS_ALLOWED=%s\nVW_HTTP_PORT=%d\n",
		c.ServerIP, vwStored, boolStr(c.VWSignupsAllowed, "true", "false"), c.VWPort)); err != nil {
		return nil, err
	} else if wrote {
		secrets = append(secrets, Secret{"Vaultwarden /admin token (SAVE — not recoverable):", vwToken})
	}

	// nextcloud
	ncPw := genPassword(20)
	if wrote, err := writeEnv("nextcloud/.env", fmt.Sprintf(
		"POSTGRES_DB=nextcloud\nPOSTGRES_USER=nextcloud\nPOSTGRES_PASSWORD=%s\nREDIS_PASSWORD=%s\n"+
			"NC_ADMIN_USER=admin\nNC_ADMIN_PASSWORD=%s\nNC_TRUSTED_DOMAINS=%s\n"+
			"NC_TRUSTED_PROXIES=172.16.0.0/12\nNC_HTTP_PORT=%d\n",
		genPassword(32), genPassword(32), ncPw, c.ServerIP, c.NCPort)); err != nil {
		return nil, err
	} else if wrote {
		secrets = append(secrets, Secret{"Nextcloud (user 'admin'):", ncPw})
	}

	// pihole
	phPw := genPassword(20)
	if wrote, err := writeEnv("pihole/.env", fmt.Sprintf(
		"TZ=%s\nPIHOLE_PASSWORD=%s\nPIHOLE_UPSTREAMS=1.1.1.1;9.9.9.9\nPIHOLE_WEB_PORT=%d\nPIHOLE_DNS_BIND=%s\n",
		c.TZ, phPw, c.PiholeWebPort, c.PiholeDNSBind)); err != nil {
		return nil, err
	} else if wrote {
		secrets = append(secrets, Secret{"Pi-hole admin:", phPw})
	}

	// pihole/custom.list — no local hostnames (apps are reached by IP:port). Kept as
	// an almost-empty file so the bind-mount has a file to mount.
	listPath := filepath.Join(repo, "pihole/custom.list")
	if !fileExists(listPath) || force {
		if err := writeFile0644(listPath,
			"# Pi-hole local DNS records (none — apps are reached by IP:port).\n"); err != nil {
			return nil, err
		}
	}

	// caddy — HTTPS per app at the server IP + port (cert SAN = the IP).
	if _, err := writeEnv("caddy/.env", fmt.Sprintf(
		"SERVER_IP=%s\nACME_EMAIL=%s\n"+
			"VAULT_UPSTREAM=host.docker.internal:%d\nCLOUD_UPSTREAM=host.docker.internal:%d\n"+
			"PIHOLE_UPSTREAM=host.docker.internal:%d\nHOME_UPSTREAM=host.docker.internal:%d\n"+
			"STIRLING_UPSTREAM=host.docker.internal:8090\nITTOOLS_UPSTREAM=host.docker.internal:8091\n"+
			"IMAGETOOLS_UPSTREAM=host.docker.internal:8092\n"+
			"VAULT_HTTPS=8443\nCLOUD_HTTPS=8444\nPIHOLE_HTTPS=8445\nHOME_HTTPS=443\n"+
			"STIRLING_HTTPS=8446\nITTOOLS_HTTPS=8447\nIMAGETOOLS_HTTPS=8448\n",
		c.ServerIP, c.ACMEEmail, c.VWPort, c.NCPort, c.PiholeWebPort, c.UIPort)); err != nil {
		return nil, err
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
		return nil, err
	}

	return secrets, nil
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
