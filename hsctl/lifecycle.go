package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// services in start order. down reverses this. caddy is last so it can proxy the rest.
var services = []string{"vaultwarden", "nextcloud", "pihole", "stirling", "it-tools", "imagetools", "caddy"}

// edgeNetwork is the shared reverse-proxy network Caddy and every app attach to (declared
// `external` in the compose files). It replaces host-published app ports: Caddy reaches each
// app by its container name over this network, so the apps expose no plaintext HTTP on the LAN.
const edgeNetwork = "homeserver-edge"

// ensureEdgeNetwork creates edgeNetwork if it isn't already present. Idempotent, and safe
// against a concurrent create. Must run before any `compose up`, because the compose files
// declare the network external (so they won't create it themselves).
func ensureEdgeNetwork() error {
	dir := repoDir()
	if err := dockerCmd(dir, "network", "inspect", edgeNetwork).Run(); err == nil {
		return nil // already exists
	}
	if out, err := dockerCombined(dir, "network", "create", edgeNetwork); err != nil {
		if !strings.Contains(out, "already exists") { // lost a create race — that's fine
			return fmt.Errorf("create docker network %q: %v\n%s", edgeNetwork, err, strings.TrimSpace(out))
		}
	}
	return nil
}

// migrateSharedNetworkEnv strips settings made obsolete by the move onto edgeNetwork so an
// existing install picks up the new behaviour without a --force re-setup: the app upstreams
// Caddy used to reach via host-published ports (now container-name defaults in
// caddy/docker-compose.yml) and the per-app host ports the apps no longer publish. A stale
// caddy/.env would otherwise still point Caddy at now-dead host ports. Best-effort + idempotent.
func migrateSharedNetworkEnv(repo string) {
	for _, m := range []struct {
		rel  string
		keys []string
	}{
		{"caddy/.env", []string{"VAULT_UPSTREAM", "CLOUD_UPSTREAM", "PIHOLE_UPSTREAM",
			"STIRLING_UPSTREAM", "ITTOOLS_UPSTREAM", "IMAGETOOLS_UPSTREAM"}},
		{"vaultwarden/.env", []string{"VW_HTTP_PORT"}},
		{"nextcloud/.env", []string{"NC_HTTP_PORT"}},
		{"pihole/.env", []string{"PIHOLE_WEB_PORT"}},
	} {
		if changed, err := removeEnvKeys(filepath.Join(repo, m.rel), m.keys...); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not migrate %s to the shared network: %v\n", m.rel, err)
		} else if changed {
			fmt.Printf("migrated %s onto the shared Caddy network (dropped obsolete host-port settings)\n", m.rel)
		}
	}
}

// coreServices need a generated .env (the tools run from compose defaults, no .env).
var coreServices = []string{"vaultwarden", "nextcloud", "pihole"}

// container names belonging to the stack (for status filtering).
var stackContainers = []string{"vaultwarden", "nextcloud", "pihole", "caddy", "stirling-pdf", "it-tools", "imagetools"}

func missingEnv() []string {
	var miss []string
	for _, s := range coreServices {
		if _, err := os.Stat(filepath.Join(repoDir(), s, ".env")); err != nil {
			miss = append(miss, s)
		}
	}
	return miss
}

func cmdUp() error {
	if m := missingEnv(); len(m) > 0 {
		return fmt.Errorf("missing .env for %v — run: hsctl setup", m)
	}
	migrateSharedNetworkEnv(repoDir())
	if err := ensureEdgeNetwork(); err != nil {
		return err
	}
	for _, s := range services {
		fmt.Printf("== up: %s ==\n", s)
		if err := dockerRun(filepath.Join(repoDir(), s), "compose", "up", "-d"); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	fmt.Println()
	if err := cmdStatus(); err != nil {
		return err
	}
	if !uiServiceActive() {
		fmt.Println("\nNote: the dashboard isn't running as a service yet — run `hsctl install`")
		fmt.Println("so it auto-starts on boot (or `hsctl ui` to just run it now).")
	}
	return nil
}

func cmdDown(volumes bool) error {
	down := []string{"compose", "down"}
	if volumes {
		down = append(down, "-v")
		fmt.Println("!! --volumes: data volumes will be DELETED")
	}
	for i := len(services) - 1; i >= 0; i-- {
		s := services[i]
		fmt.Printf("== down: %s ==\n", s)
		if err := dockerRun(filepath.Join(repoDir(), s), down...); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

func cmdStatus() error {
	args := []string{"ps", "--format", "table {{.Names}}\t{{.Status}}"}
	for _, n := range stackContainers {
		args = append(args, "--filter", "name="+n)
	}
	return dockerRun(repoDir(), args...)
}

func cmdGetCA() error {
	out, err := dockerOut(repoDir(), "exec", "caddy", "cat",
		"/data/caddy/pki/authorities/local/root.crt")
	if err != nil {
		return fmt.Errorf("reading CA from caddy (is it up?): %w", err)
	}
	dst := filepath.Join(repoDir(), "caddy-root-ca.crt")
	if err := os.WriteFile(dst, []byte(out+"\n"), 0644); err != nil {
		return err
	}
	fmt.Println("wrote", dst)
	fmt.Println("install it as a trusted CA on each device (see ONBOARDING.md)")
	return nil
}
