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
		Args:  cobra.NoArgs, RunE: locked("secrets rotate-vw-admin", 0, func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			return rotateVWAdmin(repo)
		})})
	s.AddCommand(&cobra.Command{Use: "set-ui-password [password]",
		Short: "Set the dashboard's admin password (random if you don't give one) and restart the dashboard",
		Args:  cobra.MaximumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			pw := ""
			if len(args) == 1 {
				pw = args[0]
			}
			return setUIPassword(repo, pw)
		}})
	return s
}

// setUIPassword writes the dashboard password (a generated one when pw is empty) and restarts the
// running dashboard, which reads it only at startup — otherwise the new password silently
// wouldn't work until the next reboot.
func setUIPassword(repo, pw string) error {
	if pw == "" {
		pw = genPassword(16)
	} else if len(pw) < 8 {
		return fmt.Errorf("pick a password of at least 8 characters (the dashboard grants a root shell)")
	}
	if err := writeFile0600(filepath.Join(repo, ".ui-password"), pw+"\n"); err != nil {
		return err
	}
	fmt.Printf("Dashboard login: user 'admin', password %s\n", pw)
	switch {
	case os.Getenv("HSCTL_FROM_DASHBOARD") != "":
		fmt.Println("It takes effect when the dashboard restarts: sudo systemctl restart hsctl-ui")
	case uiServiceActive():
		cmd := privCmd("systemctl", "restart", "hsctl-ui.service")
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restart the dashboard: %w (run: sudo systemctl restart hsctl-ui)", err)
		}
		fmt.Println("Restarted the dashboard — sign in again with the new password.")
	default:
		fmt.Println("(The dashboard service isn't running; the next `hsctl ui` / `sudo hsctl install` uses it.)")
	}
	return nil
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
	if err := requireReadable(repo, "vaultwarden/.env"); err != nil {
		return err
	}
	token := genPassword(40)
	if err := setEnvKey(envPath, "VW_ADMIN_TOKEN", escapeDollarsForCompose(argon2idPHC(token))); err != nil {
		return err
	}
	fmt.Println("New Vaultwarden /admin token — SAVE THIS NOW, it is NOT recoverable:")
	fmt.Println("\n    " + token + "\n")
	fmt.Println("Stored as an Argon2id hash in vaultwarden/.env. Recreating the container...")
	// vaultwarden's compose file attaches to the external edge network, so it must exist before
	// `compose up` — otherwise recreate fails and the (already-written) new token wouldn't take.
	if err := ensureEdgeNetwork(); err != nil {
		return err
	}
	if err := dockerRun(filepath.Join(repo, "vaultwarden"), composeUpArgs("--force-recreate", "vaultwarden")...); err != nil {
		return fmt.Errorf("recreate vaultwarden: %w", err)
	}
	fmt.Println("done — log in at " + vwAdminURL(envPath) + " with the token above.")
	return nil
}

// vwAdminURL builds the real /admin URL from VW_DOMAIN in vaultwarden/.env (e.g.
// https://192.168.0.150:8443/admin), falling back to a placeholder if it can't be read.
func vwAdminURL(envPath string) string {
	if kv, err := readKV(envPath); err == nil {
		if d := strings.TrimRight(kv["VW_DOMAIN"], "/"); d != "" {
			return d + "/admin"
		}
	}
	return "https://<server-ip>:8443/admin"
}

// secretsShow reads each login from the live .env files (+ the dashboard password file).
func secretsShow(repo string) error {
	type item struct{ label, file, key string }
	var any bool
	var blocked []string // files that exist but we can't read (left root-owned)
	for _, it := range []item{
		{"Vaultwarden /admin token", "vaultwarden/.env", "VW_ADMIN_TOKEN"},
		{"Nextcloud (user 'admin')", "nextcloud/.env", "NC_ADMIN_PASSWORD"},
		{"Pi-hole admin", "pihole/.env", "PIHOLE_PASSWORD"},
	} {
		path := filepath.Join(repo, it.file)
		kv, err := readKV(path)
		if err != nil {
			if unreadable(path) {
				fmt.Printf("%-28s (can't read %s)\n", it.label+":", it.file)
				blocked = append(blocked, it.file)
			}
			continue
		}
		v := kv[it.key]
		if v == "" {
			continue
		}
		// The Vaultwarden token is stored Argon2-hashed — don't print the hash as if
		// it were the login; it's not reversible.
		if it.key == "VW_ADMIN_TOKEN" && strings.Contains(v, "argon2") {
			v = "(Argon2-hashed; shown once at setup — reset with: hsctl secrets rotate-vw-admin)"
		} else if strings.HasPrefix(v, "change-me") {
			// A .env copied from its .env.example: a placeholder, not a generated secret.
			v += "   <- a PLACEHOLDER from the .env.example — set a real one (see docs/security.md)"
		}
		fmt.Printf("%-28s %s\n", it.label+":", v)
		any = true
	}
	if b, err := os.ReadFile(filepath.Join(repo, resticPassFile)); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			fmt.Printf("%-28s %s (keep a copy off this machine)\n", "Backup password:", v)
			any = true
		}
	} else if unreadable(filepath.Join(repo, resticPassFile)) {
		fmt.Printf("%-28s (can't read %s)\n", "Backup password:", resticPassFile)
		blocked = append(blocked, resticPassFile)
	}
	uiPass := filepath.Join(repo, ".ui-password")
	if b, err := os.ReadFile(uiPass); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			fmt.Printf("%-28s %s (user 'admin')\n", "Dashboard:", v)
			any = true
		}
	} else if unreadable(uiPass) {
		fmt.Printf("%-28s (can't read .ui-password)\n", "Dashboard:")
		blocked = append(blocked, ".ui-password")
	}
	if len(blocked) > 0 {
		fmt.Println()
		return errUnreadable(repo, blocked)
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
