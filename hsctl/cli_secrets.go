package main

import "github.com/spf13/cobra"

// secretsCmd shows the generated logins, read live from the .env files (the source of
// truth the stack already needs) — there's no separate plaintext copy to manage.
func (a *app) secretsCmd() *cobra.Command {
	s := &cobra.Command{Use: "secrets", Short: "Show the generated logins (from the .env files)"}
	s.AddCommand(&cobra.Command{Use: "show", Short: "Print the logins (read from the .env files)",
		Args: cobra.NoArgs, RunE: a.inRepo(func(*cobra.Command, []string) error { return a.secrets.Show() })})
	s.AddCommand(&cobra.Command{Use: "rotate-vw-admin",
		Short: "Generate a NEW Vaultwarden /admin token, store it Argon2-hashed, recreate the container",
		Args:  cobra.NoArgs, RunE: a.inRepo(a.locked("secrets rotate-vw-admin", 0, func(*cobra.Command, []string) error {
			return a.secrets.RotateVWAdmin()
		}))})
	s.AddCommand(&cobra.Command{Use: "set-ui-password [password]",
		Short: "Set the dashboard's admin password (random if you don't give one) and restart the dashboard",
		Args:  cobra.MaximumNArgs(1), RunE: a.inRepo(func(_ *cobra.Command, args []string) error {
			pw := ""
			if len(args) == 1 {
				pw = args[0]
			}
			return a.secrets.SetUIPassword(pw)
		})})
	return s
}
