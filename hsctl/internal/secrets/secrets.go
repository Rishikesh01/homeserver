// Package secrets shows the stack's generated logins and changes the ones hsctl owns: the
// Vaultwarden admin token and the dashboard password.
package secrets

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secret"
)

// Docker recreates Vaultwarden with a new token.
type Docker interface {
	Run(dir string, args ...string) error
	EnsureNetwork(name string) error
	ComposeUpArgs(extra ...string) []string
}

// Privileged runs a command as root (restarting the dashboard).
type Privileged interface {
	Command(name string, args ...string) *exec.Cmd
}

// Service shows and changes one repo's logins.
type Service struct {
	Repo   paths.Repo
	Docker Docker
	Priv   Privileged
	// DashboardActive reports whether the dashboard runs as a service (and so must be restarted
	// to pick up a new password).
	DashboardActive func() bool
	Out             io.Writer
}

// New returns the secrets service for repo.
func New(repo paths.Repo, dk Docker, priv Privileged) *Service {
	return &Service{Repo: repo, Docker: dk, Priv: priv, DashboardActive: func() bool { return false }, Out: os.Stdout}
}

// UIPassword returns the dashboard's admin password: $HSCTL_UI_PASSWORD, or .ui-password,
// generating and saving one on first use.
func (s *Service) UIPassword() (string, error) {
	if p := os.Getenv("HSCTL_UI_PASSWORD"); p != "" {
		return p, nil
	}
	path := s.Repo.Path(paths.UIPassword)
	if b, err := os.ReadFile(path); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p, nil
		}
	}
	// Never mint a new password over one we merely can't read: the running service would keep the
	// old one and the file would no longer say what it is.
	if fsutil.Unreadable(path) {
		return "", fsutil.ErrUnreadable(s.Repo.Root, []string{paths.UIPassword})
	}
	p := secret.Password(16)
	if err := fsutil.WritePrivate(path, p+"\n"); err != nil {
		return "", fmt.Errorf("saving the dashboard password: %w", err)
	}
	return p, nil
}

// SetUIPassword writes the dashboard password (a generated one when pw is empty) and restarts the
// running dashboard, which reads it only at startup — otherwise the new password silently wouldn't
// work until the next reboot.
func (s *Service) SetUIPassword(pw string) error {
	if pw == "" {
		pw = secret.Password(16)
	} else if len(pw) < 8 {
		return fmt.Errorf("pick a password of at least 8 characters (the dashboard grants a root shell)")
	}
	if err := fsutil.WritePrivate(s.Repo.Path(paths.UIPassword), pw+"\n"); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "Dashboard login: user 'admin', password %s\n", pw)
	switch {
	case os.Getenv("HSCTL_FROM_DASHBOARD") != "":
		fmt.Fprintln(s.Out, "It takes effect when the dashboard restarts: sudo systemctl restart hsctl-ui")
	case s.DashboardActive():
		cmd := s.Priv.Command("systemctl", "restart", "hsctl-ui.service")
		cmd.Stdout, cmd.Stderr, cmd.Stdin = s.Out, s.Out, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restart the dashboard: %w (run: sudo systemctl restart hsctl-ui)", err)
		}
		fmt.Fprintln(s.Out, "Restarted the dashboard — sign in again with the new password.")
	default:
		fmt.Fprintln(s.Out, "(The dashboard service isn't running; the next `hsctl ui` / `sudo hsctl install` uses it.)")
	}
	return nil
}

// RotateVWAdmin issues a fresh /admin token, stores only its Argon2id hash in vaultwarden/.env (so
// the plaintext is never on disk), prints the plaintext once, and recreates the container so it
// takes effect. Heeds Vaultwarden's "don't use a plaintext ADMIN_TOKEN" warning.
func (s *Service) RotateVWAdmin() error {
	envPath := s.Repo.Path("vaultwarden", ".env")
	if !fsutil.Exists(envPath) {
		return fmt.Errorf("%s not found — run: hsctl setup", envPath)
	}
	if err := fsutil.RequireReadable(s.Repo.Root, "vaultwarden/.env"); err != nil {
		return err
	}
	token := secret.Password(40)
	if err := envfile.Set(envPath, "VW_ADMIN_TOKEN", secret.EscapeDollars(secret.Argon2idPHC(token))); err != nil {
		return err
	}
	fmt.Fprintln(s.Out, "New Vaultwarden /admin token — SAVE THIS NOW, it is NOT recoverable:")
	fmt.Fprintln(s.Out, "\n    "+token+"\n")
	fmt.Fprintln(s.Out, "Stored as an Argon2id hash in vaultwarden/.env. Recreating the container...")
	// vaultwarden's compose file attaches to the external edge network, so it must exist before
	// `compose up` — otherwise recreate fails and the (already-written) new token wouldn't take.
	if err := s.Docker.EnsureNetwork(layout.EdgeNetwork); err != nil {
		return err
	}
	if err := s.Docker.Run(s.Repo.Path("vaultwarden"), s.Docker.ComposeUpArgs("--force-recreate", layout.Vaultwarden)...); err != nil {
		return fmt.Errorf("recreate vaultwarden: %w", err)
	}
	fmt.Fprintln(s.Out, "done — log in at "+vwAdminURL(envPath)+" with the token above.")
	return nil
}

// vwAdminURL builds the real /admin URL from VW_DOMAIN in vaultwarden/.env (e.g.
// https://192.168.0.150:8443/admin), falling back to a placeholder if it can't be read.
func vwAdminURL(envPath string) string {
	if kv, err := envfile.Read(envPath); err == nil {
		if d := strings.TrimRight(kv["VW_DOMAIN"], "/"); d != "" {
			return d + "/admin"
		}
	}
	return "https://<server-ip>:8443/admin"
}

// Show prints each login, read from the live .env files (the source of truth the stack already
// needs), plus the backup and dashboard passwords — there's no separate plaintext copy to manage.
func (s *Service) Show() error {
	type item struct{ label, file, key string }
	var any bool
	var blocked []string // files that exist but we can't read (left root-owned)
	for _, it := range []item{
		{"Vaultwarden /admin token", "vaultwarden/.env", "VW_ADMIN_TOKEN"},
		{"Nextcloud (user 'admin')", "nextcloud/.env", "NC_ADMIN_PASSWORD"},
		{"Pi-hole admin", "pihole/.env", "PIHOLE_PASSWORD"},
	} {
		path := s.Repo.Path(it.file)
		kv, err := envfile.Read(path)
		if err != nil {
			if fsutil.Unreadable(path) {
				fmt.Fprintf(s.Out, "%-28s (can't read %s)\n", it.label+":", it.file)
				blocked = append(blocked, it.file)
			}
			continue
		}
		v := kv[it.key]
		if v == "" {
			continue
		}
		// The Vaultwarden token is stored Argon2-hashed — don't print the hash as if it were the
		// login; it's not reversible.
		if it.key == "VW_ADMIN_TOKEN" && strings.Contains(v, "argon2") {
			v = "(Argon2-hashed; shown once at setup — reset with: hsctl secrets rotate-vw-admin)"
		} else if strings.HasPrefix(v, "change-me") {
			// A .env copied from its .env.example: a placeholder, not a generated secret.
			v += "   <- a PLACEHOLDER from the .env.example — set a real one (see docs/security.md)"
		}
		fmt.Fprintf(s.Out, "%-28s %s\n", it.label+":", v)
		any = true
	}
	for _, f := range []struct{ label, rel, note string }{
		{"Backup password:", paths.ResticPassword, " (keep a copy off this machine)"},
		{"Dashboard:", paths.UIPassword, " (user 'admin')"},
	} {
		path := s.Repo.Path(f.rel)
		if b, err := os.ReadFile(path); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				fmt.Fprintf(s.Out, "%-28s %s%s\n", f.label, v, f.note)
				any = true
			}
		} else if fsutil.Unreadable(path) {
			fmt.Fprintf(s.Out, "%-28s (can't read %s)\n", f.label, f.rel)
			blocked = append(blocked, f.rel)
		}
	}
	if len(blocked) > 0 {
		fmt.Fprintln(s.Out)
		return fsutil.ErrUnreadable(s.Repo.Root, blocked)
	}
	if !any {
		fmt.Fprintln(s.Out, "No logins found — has the stack been set up? Run: hsctl setup")
		return nil
	}
	fmt.Fprintln(s.Out, "\nNote: these are the secrets GENERATED AT SETUP (read from the .env files). If")
	fmt.Fprintln(s.Out, "you've since changed a password inside an app (e.g. your Nextcloud or Vaultwarden")
	fmt.Fprintln(s.Out, "login), the new one is NOT shown here — the .env value is the original.")
	fmt.Fprintln(s.Out, "These are plaintext on disk: protect them with full-disk encryption, and save")
	fmt.Fprintln(s.Out, "them into Vaultwarden.")
	return nil
}
