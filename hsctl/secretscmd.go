package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const secretsListFile = ".secrets.txt"

// secretsCmd shows the generated logins (read live from the .env files, so it always
// works) and can shred a legacy .secrets.txt convenience file. The canonical secrets
// live in each service's .env — there is no separate plaintext copy to manage.
func secretsCmd() *cobra.Command {
	s := &cobra.Command{Use: "secrets", Short: "Show the generated logins (from the .env files)"}
	s.AddCommand(
		&cobra.Command{Use: "show", Short: "Print the logins (read from the .env files)",
			Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return secretsShow(repoDir()) }},
		&cobra.Command{Use: "shred", Short: "Securely remove a legacy " + secretsListFile + " file (if present)",
			Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return secretsShred(repoDir()) }},
	)
	return s
}

// secretsShow reads each login from the live .env files (+ the dashboard password file).
// It keeps working after `secrets shred`, because it never depended on .secrets.txt.
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

// secretsShred removes a legacy .secrets.txt (older setups wrote one), overwriting it
// with random data first. Note: overwriting a file's blocks is NOT a guaranteed erase on
// SSDs / copy-on-write filesystems — full-disk encryption (LUKS) is the real protection.
func secretsShred(repo string) error {
	path := filepath.Join(repo, secretsListFile)
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("no %s to remove — logins live only in the .env files now.\n", secretsListFile)
			fmt.Println("(those are plaintext on disk by necessity; full-disk encryption protects them.)")
			return nil
		}
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	for pass := 0; pass < 3; pass++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		if _, err := io.CopyN(f, rand.Reader, fi.Size()); err != nil {
			f.Close()
			return err
		}
		f.Sync()
	}
	f.Close()
	if err := os.Remove(path); err != nil {
		return err
	}
	fmt.Printf("shredded %s (3 random passes, then removed).\n", secretsListFile)
	fmt.Println("Reminder: the logins still exist in the .env files (needed by the stack) —")
	fmt.Println("full-disk encryption is what actually protects them at rest.")
	return nil
}
