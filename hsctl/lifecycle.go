package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	warn := func(rel string, err error) {
		fmt.Fprintf(os.Stderr, "warning: could not migrate %s to the shared network: %v\n", rel, err)
	}
	note := func(rel string) {
		fmt.Printf("migrated %s onto the shared Caddy network (dropped obsolete host-port settings)\n", rel)
	}

	// caddy/.env: drop only the STALE host-gateway upstreams (host.docker.internal:<port>). A
	// user's deliberate container-name override (which caddy/.env.example explicitly permits, for
	// a renamed service) doesn't contain host.docker.internal, so it's left intact.
	upstream := map[string]bool{
		"VAULT_UPSTREAM": true, "CLOUD_UPSTREAM": true, "PIHOLE_UPSTREAM": true,
		"STIRLING_UPSTREAM": true, "ITTOOLS_UPSTREAM": true, "IMAGETOOLS_UPSTREAM": true,
	}
	if changed, err := removeEnvLinesMatching(filepath.Join(repo, "caddy/.env"), func(k, v string) bool {
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
		if changed, err := removeEnvKeys(filepath.Join(repo, m.rel), m.key); err != nil {
			warn(m.rel, err)
		} else if changed {
			note(m.rel)
		}
	}
}

// coreServices need a generated .env (the tools run from compose defaults, no .env).
var coreServices = []string{"vaultwarden", "nextcloud", "pihole"}

// envFiles are the generated per-service .env files (repo-relative). docker compose reads them on
// every up/down, so an unreadable one has to be reported before compose fails on it.
var envFiles = []string{"vaultwarden/.env", "nextcloud/.env", "pihole/.env", "caddy/.env"}

// container names belonging to the stack (for status filtering).
var stackContainers = []string{"vaultwarden", "nextcloud", "pihole", "caddy", "stirling-pdf", "it-tools", "imagetools"}

func missingEnv() []string { return missingEnvIn(repoDir()) }

// missingEnvFor lists what `up` needs but setup hasn't generated: the .env of every ENABLED core
// app, plus Caddy's (without it Caddy would issue its certificate for a blank address). A switched
// off app — Pi-hole, say — doesn't need its .env to exist.
func missingEnvFor(repo string, c Config) []string {
	var miss []string
	for _, s := range append(append([]string{}, coreServices...), "caddy") {
		if c.IsDisabled(s) {
			continue
		}
		if _, err := os.Stat(filepath.Join(repo, s, ".env")); err != nil {
			miss = append(miss, s)
		}
	}
	return miss
}

// upServices runs `compose up -d` for each service in order, carrying on past a failure so one
// broken app (say Pi-hole unable to bind :53) can't keep the rest — and Caddy, the only way to
// the dashboard — down. Caddy always goes last. run starts one service; failures are returned.
func upServices(order []string, run func(svc string) error) (failed []string) {
	for _, svc := range order {
		if err := run(svc); err != nil {
			failed = append(failed, svc)
		}
	}
	return failed
}

// missingEnvIn lists the core services under repo that have no .env yet (i.e. setup hasn't
// been run for them). Empty means the stack is configured.
func missingEnvIn(repo string) []string {
	var miss []string
	for _, s := range coreServices {
		if _, err := os.Stat(filepath.Join(repo, s, ".env")); err != nil {
			miss = append(miss, s)
		}
	}
	return miss
}

func cmdUp() error {
	if err := requireReadable(repoDir(), envFiles...); err != nil {
		return err
	}
	cfg := LoadConfig(repoDir())
	if m := missingEnvFor(repoDir(), cfg); len(m) > 0 {
		return fmt.Errorf("missing .env for %s — run: hsctl setup", strings.Join(m, ", "))
	}
	migrateSharedNetworkEnv(repoDir())
	if err := ensureEdgeNetwork(); err != nil {
		return err
	}
	if len(cfg.DisabledApps) > 0 {
		fmt.Printf("(skipping disabled apps: %s — `hsctl apps enable <name>` to turn one on)\n", strings.Join(cfg.DisabledApps, ", "))
	}
	failed := upServices(enabledServices(cfg), func(svc string) error {
		fmt.Printf("== up: %s ==\n", svc)
		err := dockerRun(filepath.Join(repoDir(), svc), "compose", "up", "-d")
		if err != nil {
			fmt.Fprintf(os.Stderr, "!! %s didn't start (%v) — carrying on with the rest\n", svc, err)
		}
		return err
	})
	// Nextcloud only reads NC_TRUSTED_DOMAINS on first install; keep its own list in step with
	// the server IP so an IP change doesn't lock it behind "untrusted domain". Best-effort: a
	// fresh Nextcloud may still be installing (and then takes the env value anyway).
	if !cfg.IsDisabled("nextcloud") {
		if added, err := ensureNextcloudTrusts(repoDir(), cfg.ServerIP); err == nil && added {
			fmt.Printf("nextcloud: added %s to its trusted domains\n", cfg.ServerIP)
		}
	}
	fmt.Println()
	if err := cmdStatus(); err != nil {
		return err
	}
	if len(failed) > 0 {
		if slices.Contains(failed, "pihole") {
			fmt.Println("\nPi-hole needs port 53. If something else on this machine holds it, bind Pi-hole to the")
			fmt.Println("LAN IP (hsctl setup --pihole-dns-bind <LAN IP>), or switch it off: hsctl apps disable pihole")
			fmt.Println("(nothing else depends on Pi-hole — devices only use it if you point them at it).")
		}
		return fmt.Errorf("%s didn't start — see the output above", strings.Join(failed, ", "))
	}
	if !uiServiceActive() {
		fmt.Println("\nNote: the dashboard isn't running as a service yet — run `sudo hsctl install`")
		fmt.Println("so it auto-starts on boot (or `hsctl ui` to just run it now).")
	}
	return nil
}

// cmdDown stops the stack. keepProxy leaves Caddy running: the dashboard is only reachable
// through it, so a stop started FROM the dashboard must keep it up or the admin is locked out
// (Start all would be unreachable without SSH). With volumes, Caddy's data — the certificate
// authority every device trusts — is then kept too.
func cmdDown(volumes, keepProxy bool) error {
	if err := requireReadable(repoDir(), envFiles...); err != nil {
		return err
	}
	down := []string{"compose", "down"}
	if volumes {
		down = append(down, "-v")
		fmt.Println("!! --volumes: data volumes will be DELETED")
	}
	for _, s := range stopOrder(keepProxy) {
		fmt.Printf("== down: %s ==\n", s)
		if err := dockerRun(filepath.Join(repoDir(), s), down...); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	if keepProxy {
		fmt.Println("\nThe apps are stopped. Caddy (and so the dashboard) is still running — start them again")
		fmt.Println("with Start all in the dashboard, or `hsctl up`. Stopped apps stay stopped after a reboot.")
	} else {
		fmt.Println("\nEverything is stopped, Caddy included, so the dashboard is unreachable until `hsctl up`.")
		fmt.Println("Stopped containers stay stopped after a reboot too.")
	}
	return nil
}

// stopOrder is the order services go down in — the reverse of the start order, so Caddy stops
// first — without Caddy when keepProxy is set. Every app is included, switched off or not, so a
// since-disabled app's leftovers stop too.
func stopOrder(keepProxy bool) []string {
	var out []string
	for _, s := range reversed(services) {
		if keepProxy && s == "caddy" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// cmdRestart stops the apps and starts the stack again. Caddy keeps running throughout (up
// leaves a running container alone), so the dashboard stays reachable.
func cmdRestart() error {
	if err := cmdDown(false, true); err != nil {
		return err
	}
	fmt.Println()
	return cmdUp()
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
	if err := writeFile0644(dst, out+"\n"); err != nil {
		return err
	}
	fmt.Println("wrote", dst)
	fmt.Println("install it as a trusted CA on each device (see ONBOARDING.md)")
	return nil
}
