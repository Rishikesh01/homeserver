package backup

import (
	"fmt"

	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secret"
)

// ensurePassword creates the repo password for `backup init`, when there isn't one yet. Only init
// does this: generating one anywhere else (as `run` and `restore` used to) replaced a LOST
// password with a new random one, so restoring onto a fresh machine failed with "wrong password"
// and the nightly backup failed every night after a `git clean`. A local repository that already
// exists is never given a new password either.
func (s *Service) ensurePassword(cfg Settings) error {
	path := s.Repo.Path(paths.ResticPassword)
	if fsutil.Exists(path) {
		return nil
	}
	if fsutil.Exists(cfg.Repo + "/config") { // a local restic repository is already there
		return fmt.Errorf("a backup repository already exists at %s, but %s is missing — "+
			"put your saved password back first:\n  hsctl backup config --password '…'", cfg.Repo, paths.ResticPassword)
	}
	if err := fsutil.WritePrivate(path, secret.Password(32)+"\n"); err != nil {
		return fmt.Errorf("save the new backup password: %w", err)
	}
	fmt.Fprintf(s.Out, "generated the backup password -> %s\n", path)
	fmt.Fprintln(s.Out, "  SAVE A COPY SOMEWHERE ELSE (Vaultwarden, or written down): without it the backups can't be")
	fmt.Fprintln(s.Out, "  decrypted. See it any time with: hsctl secrets show")
	return nil
}

// RequirePassword fails, with what to do, when there's no repo password — for every backup
// command but init.
func (s *Service) RequirePassword() error {
	if fsutil.Exists(s.Repo.Path(paths.ResticPassword)) {
		return nil
	}
	return fmt.Errorf("no backup password (%s).\n"+
		"  Have backups already? Put your saved password back: hsctl backup config --password '…'\n"+
		"  Starting fresh? hsctl backup init creates the repository and a password.", paths.ResticPassword)
}
