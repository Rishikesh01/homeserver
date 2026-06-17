package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// secretsCmd shows the generated logins, read live from the .env files (the source of
// truth the stack already needs) — there's no separate plaintext copy to manage.
func secretsCmd() *cobra.Command {
	s := &cobra.Command{Use: "secrets", Short: "Show the generated logins (from the .env files)"}
	s.AddCommand(&cobra.Command{Use: "show", Short: "Print the logins (read from the .env files)",
		Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			return secretsShow(repo)
		}})
	s.AddCommand(&cobra.Command{Use: "rotate-vw-admin",
		Short: "Generate a NEW Vaultwarden /admin token, store it Argon2-hashed, recreate the container",
		Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			return rotateVWAdmin(repo)
		}})
	return s
}

// rotateVWAdmin issues a fresh /admin token, stores only its Argon2id hash in
// vaultwarden/.env (so the plaintext is never on disk), prints the plaintext once, and
// recreates the container so it takes effect. Heeds Vaultwarden's "don't use a plaintext
// ADMIN_TOKEN" warning.
func rotateVWAdmin(repo string) error {
	envPath := filepath.Join(repo, "vaultwarden", ".env")
	if !fileExists(envPath) {
		return fmt.Errorf("%s not found — run: hsctl setup", envPath)
	}
	token := genPassword(40)
	if err := setEnvKey(envPath, "VW_ADMIN_TOKEN", escapeDollarsForCompose(argon2idPHC(token))); err != nil {
		return err
	}
	fmt.Println("New Vaultwarden /admin token — SAVE THIS NOW, it is NOT recoverable:")
	fmt.Println("\n    " + token + "\n")
	fmt.Println("Stored as an Argon2id hash in vaultwarden/.env. Recreating the container...")
	if err := dockerRun(filepath.Join(repo, "vaultwarden"), "compose", "up", "-d", "--force-recreate", "vaultwarden"); err != nil {
		return fmt.Errorf("recreate vaultwarden: %w", err)
	}
	fmt.Println("done — log in at https://<server-ip>:8443/admin with the token above.")
	return nil
}

// secretsShow reads each login from the live .env files (+ the dashboard password file).
func secretsShow(repo string) error {
	type item struct{ label, file, key string }
	var any bool
	for _, it := range []item{
		{"Vaultwarden /admin token", "vaultwarden/.env", "VW_ADMIN_TOKEN"},
		{"Nextcloud (user 'admin')", "nextcloud/.env", "NC_ADMIN_PASSWORD"},
		{"Pi-hole admin", "pihole/.env", "PIHOLE_PASSWORD"},
	} {
		if kv, err := readKV(filepath.Join(repo, it.file)); err == nil {
			v := kv[it.key]
			if v == "" {
				continue
			}
			// The Vaultwarden token is stored Argon2-hashed — don't print the hash as if
			// it were the login; it's not reversible.
			if it.key == "VW_ADMIN_TOKEN" && strings.Contains(v, "argon2") {
				v = "(Argon2-hashed; shown once at setup — reset with: hsctl secrets rotate-vw-admin)"
			}
			fmt.Printf("%-28s %s\n", it.label+":", v)
			any = true
		}
	}
	if b, err := os.ReadFile(filepath.Join(repo, ".ui-password")); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			fmt.Printf("%-28s %s (user 'admin')\n", "Dashboard:", v)
			any = true
		}
	}
	if !any {
		fmt.Println("No logins found — has the stack been set up? Run: hsctl setup")
		return nil
	}
	fmt.Println("\nNote: these are the secrets GENERATED AT SETUP (read from the .env files). If")
	fmt.Println("you've since changed a password inside an app (e.g. your Nextcloud or Vaultwarden")
	fmt.Println("login), the new one is NOT shown here — the .env value is the original.")
	fmt.Println("These are plaintext on disk: protect them with full-disk encryption, and save")
	fmt.Println("them into Vaultwarden.")
	return nil
}
