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
		Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return secretsShow(repoDir()) }})
	return s
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
			if v := kv[it.key]; v != "" {
				fmt.Printf("%-28s %s\n", it.label+":", v)
				any = true
			}
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
