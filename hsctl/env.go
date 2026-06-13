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
	vwToken := genPassword(40)
	if wrote, err := writeEnv("vaultwarden/.env", fmt.Sprintf(
		"VW_DOMAIN=https://%s\nVW_ADMIN_TOKEN=%s\nVW_SIGNUPS_ALLOWED=%s\nVW_HTTP_PORT=%d\n",
		c.VaultHost, vwToken, boolStr(c.VWSignupsAllowed, "true", "false"), c.VWPort)); err != nil {
		return nil, err
	} else if wrote {
		secrets = append(secrets, Secret{"Vaultwarden /admin token:", vwToken})
	}

	// nextcloud
	ncPw := genPassword(20)
	if wrote, err := writeEnv("nextcloud/.env", fmt.Sprintf(
		"POSTGRES_DB=nextcloud\nPOSTGRES_USER=nextcloud\nPOSTGRES_PASSWORD=%s\nREDIS_PASSWORD=%s\n"+
			"NC_ADMIN_USER=admin\nNC_ADMIN_PASSWORD=%s\nNC_TRUSTED_DOMAINS=%s %s\n"+
			"NC_TRUSTED_PROXIES=172.16.0.0/12\nNC_HTTP_PORT=%d\n",
		genPassword(32), genPassword(32), ncPw, c.ServerIP, c.CloudHost, c.NCPort)); err != nil {
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
			"VAULT_HTTPS=8443\nCLOUD_HTTPS=8444\nPIHOLE_HTTPS=8445\nHOME_HTTPS=443\n",
		c.ServerIP, c.ACMEEmail, c.VWPort, c.NCPort, c.PiholeWebPort, c.UIPort)); err != nil {
		return nil, err
	}

	// personalized handout
	welcome := fmt.Sprintf(`Homeserver — quick reference
  Dashboard / home portal:        https://%[8]s   /  http://%[1]s:%[9]d
  Install the certificate first:  http://%[1]s/        (download root.crt)
  Passwords (Vaultwarden):        http://%[1]s:%[2]d   /  https://%[3]s
  Files (Nextcloud):              http://%[1]s:%[4]d   /  https://%[5]s
  Pi-hole admin:                  http://%[1]s:%[6]d/admin  /  https://%[7]s
Step-by-step per device: see ONBOARDING.md
`, c.ServerIP, c.VWPort, c.VaultHost, c.NCPort, c.CloudHost, c.PiholeWebPort, c.PiholeHost,
		c.HomeHost, c.UIPort)
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
