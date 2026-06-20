package main

// Post-restore hostname fixup. Vaultwarden and Nextcloud bake their public hostname into
// data/config, so a snapshot restored onto a different host (home <-> cloud) still points at
// the OLD origin until we repoint it. This file does that repointing, idempotently and
// reversibly, and is driven by the migration orchestrator at the right moments:
//   - applyVaultwardenFixup runs while the stack is DOWN (the DOMAIN env takes effect when
//     `compose up -d` recreates the container).
//   - applyNextcloudFixup runs while the stack is UP (occ needs the app + DB running).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Caddy serves each app over HTTPS at the server host on these fixed ports (env.go / caddy/.env).
// VW_DOMAIN and the Nextcloud URLs are the PUBLIC (Caddy) origin, so they use these ports —
// never the internal container host-ports.
const (
	vwHTTPSPort = 8443
	ncHTTPSPort = 8444
)

// hostIdentity is the public identity of the stack on a given host (the home LAN IP, or the
// cloud tailnet host). Migration rewrites the apps' baked-in hostnames between these.
type hostIdentity struct {
	Host  string // bare host for Nextcloud trusted_domains (no port): LAN IP or tailnet name/IP
	VWURL string // Vaultwarden DOMAIN: https://<host>:8443
	NCURL string // Nextcloud base URL:  https://<host>:8444
}

func identityFor(host string) hostIdentity {
	return hostIdentity{
		Host:  host,
		VWURL: fmt.Sprintf("https://%s:%d", host, vwHTTPSPort),
		NCURL: fmt.Sprintf("https://%s:%d", host, ncHTTPSPort),
	}
}

// homeIdentity is the stack's identity on the home box (its LAN IP).
func homeIdentity(cfg Config) hostIdentity { return identityFor(cfg.ServerIP) }

// cloudIdentity is the stack's identity on the cloud VM (its tailnet host/IP).
func cloudIdentity(host string) hostIdentity { return identityFor(host) }

// applyVaultwardenFixup points Vaultwarden at id. The DOMAIN env var governs (this stack's
// image has no config.json by default); we also patch config.json's "domain" IF an admin
// created one via the panel, so a stale panel value can't override the env. Run with the
// stack DOWN — the env takes effect on the next `compose up -d`, which recreates the container.
func applyVaultwardenFixup(repo string, id hostIdentity) error {
	if err := setEnvKV(repo, "vaultwarden/.env", "VW_DOMAIN", id.VWURL); err != nil {
		return fmt.Errorf("set VW_DOMAIN: %w", err)
	}
	if mp, err := dockerOut(repo, "volume", "inspect", "-f", "{{.Mountpoint}}", "vaultwarden_vw-data"); err == nil && mp != "" {
		if err := patchVWConfigDomain(filepath.Join(mp, "config.json"), id.VWURL); err != nil {
			return fmt.Errorf("patch vaultwarden config.json: %w", err)
		}
	}
	fmt.Printf("  vaultwarden: DOMAIN -> %s\n", id.VWURL)
	return nil
}

// patchVWConfigDomain sets the "domain" field in Vaultwarden's config.json (inside vw-data) to
// newURL IF the file exists and already has that key — so a domain an admin saved via the
// panel (which would otherwise override the DOMAIN env var) agrees with the migrated URL.
// Absent file or absent key => nothing to do. All other keys are preserved. Pure (path in),
// so it is unit-tested.
func patchVWConfigDomain(configPath, newURL string) error {
	b, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no panel-saved config; the DOMAIN env governs
	}
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}
	if _, ok := m["domain"]; !ok {
		return nil
	}
	m["domain"] = newURL
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if fi, err := os.Stat(configPath); err == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(configPath, append(out, '\n'), mode)
}

// applyNextcloudFixup makes the running Nextcloud accept and serve requests for id.Host. It
// must run with nextcloud-app UP (occ needs the app + DB). trusted_domains is additive +
// idempotent: home and cloud hosts can both stay trusted, so move-back needs no undo. The host
// is added WITHOUT a port — Nextcloud ignores the port when the trusted domain has none, which
// matches the existing home config (bare LAN IP).
func applyNextcloudFixup(repo string, id hostIdentity) error {
	if err := waitNextcloudReady(repo, 120*time.Second); err != nil {
		return err
	}
	occRun := func(args ...string) error {
		return dockerRun(repo, append([]string{"exec", "-u", "www-data", "nextcloud-app", "php", "occ"}, args...)...)
	}
	cur, err := dockerOut(repo, "exec", "-u", "www-data", "nextcloud-app", "php", "occ", "config:system:get", "trusted_domains")
	if err != nil {
		return fmt.Errorf("read trusted_domains: %w", err)
	}
	idx := nextTrustedDomainIndex(strings.Fields(cur), id.Host)
	if idx < 0 {
		fmt.Printf("  nextcloud: %s already trusted\n", id.Host)
	} else {
		if err := occRun("config:system:set", "trusted_domains", strconv.Itoa(idx), "--value="+id.Host); err != nil {
			return fmt.Errorf("add trusted_domain %s: %w", id.Host, err)
		}
		fmt.Printf("  nextcloud: trusted_domains[%d] = %s\n", idx, id.Host)
	}
	if err := occRun("config:system:set", "overwriteprotocol", "--value=https"); err != nil {
		return fmt.Errorf("set overwriteprotocol: %w", err)
	}
	return nil
}

// nextTrustedDomainIndex returns the array index at which to add host to an existing
// trusted_domains list, or -1 if host is already present (nothing to do). Pure, so unit-tested.
func nextTrustedDomainIndex(domains []string, host string) int {
	for _, d := range domains {
		if d == host {
			return -1
		}
	}
	return len(domains)
}

// waitNextcloudReady blocks until `occ status` reports the instance installed (app + DB up),
// or the timeout elapses. Restoring + recreating the containers takes a little while.
func waitNextcloudReady(repo string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		out, err := dockerOut(repo, "exec", "-u", "www-data", "nextcloud-app", "php", "occ", "status")
		if err == nil && strings.Contains(out, "installed: true") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("nextcloud not ready after %s (last occ status err: %v)", timeout, err)
		}
		time.Sleep(3 * time.Second)
	}
}
