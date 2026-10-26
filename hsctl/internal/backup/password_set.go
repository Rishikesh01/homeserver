package backup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
)

// minNewPassword is the shortest backup password hsctl will set as a NEW one (a password you're
// putting back is whatever it was).
const minNewPassword = 12

// passwordState is what a repository says to a password.
type passwordState int

const (
	repoAbsent    passwordState = iota // no repository at the destination yet
	repoOpens                          // the password opens it
	repoWrongPass                      // there's a repository, and the password doesn't open it
)

// probePassword asks restic whether passFile opens repository. A repository restic can't reach at
// all (disk not mounted, host down) is an error, not an answer.
func (s *Service) probePassword(repository, passFile string) (passwordState, error) {
	c := exec.Command("restic", "cat", "config")
	c.Env = append(s.Restic.Env(repository), "RESTIC_PASSWORD_FILE="+passFile) // the last one wins
	out, err := c.CombinedOutput()
	o := strings.ToLower(string(out))
	switch {
	case err == nil:
		return repoOpens, nil
	case restic.RepoMissing(string(out)):
		return repoAbsent, nil
	case strings.Contains(o, "wrong password") || strings.Contains(o, "no key found"):
		return repoWrongPass, nil
	}
	return 0, fmt.Errorf("can't reach the backup repository %s: %s", repository, FirstLine(string(out)))
}

// SetPassword sets the backup password, doing whatever keeps the backups readable:
//   - no repository at the destination yet: it's saved, and `backup init` creates the repository
//     with it;
//   - the new password opens the repository: you're putting your saved password back — saved;
//   - the SAVED password opens it: this is a change — the repository's own password is changed
//     (`restic key passwd`), on the off-site replica too (they share it), and only then is the new
//     one saved. Writing the file alone would leave the backups locked behind the old password.
//
// Anything else — neither password opens the repository — is refused.
func (s *Service) SetPassword(cfg Settings, pw string, w io.Writer) error {
	if pw == "" || strings.ContainsAny(pw, "\r\n") {
		return fmt.Errorf("the password can't be empty or span lines")
	}
	if err := s.Restic.Require(); err != nil {
		return err
	}
	passFile := s.Repo.Path(paths.ResticPassword)
	// The new password goes to restic in a file, never on a command line other users could read.
	tmp, err := os.CreateTemp(s.Repo.Root, ".hsctl-tmp-pass-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err == nil {
		_, err = tmp.WriteString(pw + "\n")
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	save := func(note string) error {
		if err := fsutil.WritePrivate(passFile, pw+"\n"); err != nil {
			return err
		}
		fmt.Fprintln(w, note)
		fmt.Fprintln(w, "Keep a copy off this machine (Vaultwarden, or written down) — without it the backups can't be read.")
		return nil
	}
	if err := s.RequireMount(cfg); err != nil {
		return err
	}
	state, err := s.probePassword(cfg.Repo, tmp.Name())
	if err != nil {
		return err
	}
	switch state {
	case repoAbsent:
		if len(pw) < minNewPassword {
			return fmt.Errorf("pick a password of at least %d characters — it encrypts every backup", minNewPassword)
		}
		return save("Saved the backup password. Initialize creates the repository with it.")
	case repoOpens:
		return save("That password opens your backups — saved.")
	}
	// A repository the new password doesn't open: a change, if the saved password opens it.
	if !fsutil.Exists(passFile) {
		return fmt.Errorf("that password doesn't open the backups at %s — enter the password they were made with", cfg.Repo)
	}
	if cur, err := s.probePassword(cfg.Repo, passFile); err != nil {
		return err
	} else if cur != repoOpens {
		return fmt.Errorf("neither that password nor the saved one opens the backups at %s — enter the password they were made with", cfg.Repo)
	}
	if len(pw) < minNewPassword {
		return fmt.Errorf("pick a password of at least %d characters — it encrypts every backup", minNewPassword)
	}
	repos := []string{cfg.Repo}
	if cfg.ReplicaRepo != "" {
		switch st, err := s.probePassword(cfg.ReplicaRepo, passFile); {
		case err != nil:
			return fmt.Errorf("the off-site replica shares the password and must change with it, but: %w", err)
		case st == repoOpens:
			repos = append(repos, cfg.ReplicaRepo)
		case st == repoWrongPass:
			return fmt.Errorf("the off-site replica %s doesn't open with the saved password, so the two can't be changed together — fix that first", cfg.ReplicaRepo)
		}
	}
	for _, r := range repos {
		fmt.Fprintf(w, "changing the password of %s...\n", r)
		c := exec.Command("restic", "key", "passwd", "--new-password-file", tmp.Name())
		c.Env = s.Restic.Env(r)
		c.Stdout, c.Stderr = w, w
		if err := c.Run(); err != nil {
			if r != cfg.Repo {
				return fmt.Errorf("changed the primary's password, but changing the replica's failed (%v) — "+
					"the saved password is still the old one, which now opens only the replica; "+
					"run this again to retry the replica", err)
			}
			return fmt.Errorf("restic key passwd: %w — nothing was changed", err)
		}
	}
	return save("Changed the backup password.")
}

// ReadPasswordArg resolves `--password -` to a line read from stdin, so the password needn't
// appear on a command line (where other users can see it in ps).
func ReadPasswordArg(v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("--password -: nothing on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
