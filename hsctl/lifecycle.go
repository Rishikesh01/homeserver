package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// services in start order. down reverses this. caddy is last so it can proxy the rest.
var services = []string{"vaultwarden", "nextcloud", "pihole", "stirling", "it-tools", "imagetools", "caddy"}

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

func cmdUp(_ []string) error {
	if m := missingEnv(); len(m) > 0 {
		return fmt.Errorf("missing .env for %v — run: hsctl setup", m)
	}
	for _, s := range services {
		fmt.Printf("== up: %s ==\n", s)
		if err := dockerRun(filepath.Join(repoDir(), s), "compose", "up", "-d"); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	fmt.Println()
	return cmdStatus(nil)
}

func cmdDown(args []string) error {
	down := []string{"compose", "down"}
	for _, a := range args {
		if a == "--volumes" || a == "-v" {
			down = append(down, "-v")
			fmt.Println("!! --volumes: data volumes will be DELETED")
		}
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

func cmdStatus(_ []string) error {
	args := []string{"ps", "--format", "table {{.Names}}\t{{.Status}}"}
	for _, n := range stackContainers {
		args = append(args, "--filter", "name="+n)
	}
	return dockerRun(repoDir(), args...)
}

func cmdGetCA(_ []string) error {
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
