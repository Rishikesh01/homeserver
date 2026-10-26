package backup

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
)

// The backup-disk guard (REQUIRE_MOUNT). The classic external-disk failure: when the backup disk
// isn't mounted, its mountpoint is just an empty directory on the system disk, so restic writes a
// repo THERE — a "successful" backup that lives on the disk it's meant to protect (and splits your
// history). With REQUIRE_MOUNT set, every repo operation refuses unless that path is on a
// different filesystem from everything the stack lives on: /, the homeserver folder, and Docker's
// data. (Comparing with / alone missed setups with /home or /var on their own partition, where an
// unmounted /home/x/backup still looked "mounted".)

// systemDevices are the filesystems a backup must NOT land on.
func systemDevices(repo string) map[uint64]string {
	devs := map[uint64]string{}
	for _, p := range []string{"/", repo, "/var/lib/docker"} {
		var st syscall.Stat_t
		if syscall.Stat(p, &st) == nil {
			if _, seen := devs[uint64(st.Dev)]; !seen {
				devs[uint64(st.Dev)] = p
			}
		}
	}
	return devs
}

// RequireMount enforces the guard. Unset = no check.
func (s *Service) RequireMount(cfg Settings) error {
	repo := s.Repo.Root
	p := cfg.RequireMount
	if p == "" {
		return nil
	}
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return fmt.Errorf("backup disk path %s is missing (is the disk plugged in?): %w\n%s", p, err, mountHint(p))
	}
	if on, same := systemDevices(repo)[uint64(st.Dev)]; same {
		return fmt.Errorf("backup disk is NOT mounted at %s — that path is on the same disk as %s, so a "+
			"backup would land on the disk it's meant to protect.\n%s", p, on, mountHint(p))
	}
	return nil
}

func mountHint(p string) string {
	return fmt.Sprintf("Mount it: the dashboard's Drives page has a \"Mount for backups\" button, or on the "+
		"server run  sudo mount /dev/<disk> %s\n(Drives-page mounts last until reboot; to mount at boot, add an "+
		"/etc/fstab line for the disk with the nofail option.)", p)
}

// LocalRepoPath returns the path of a local restic repository, or "" for a remote one
// (sftp:, s3:, rest:, …).
func LocalRepoPath(repository string) string {
	if filepath.IsAbs(repository) {
		return filepath.Clean(repository)
	}
	return ""
}

// GuardFor suggests the REQUIRE_MOUNT for a local repository on a removable-style mountpoint
// (/mnt/<disk>, /media/<user>/<disk>, /run/media/<user>/<disk>): the disk's mountpoint, so a backup
// can't land on the system disk when it's unplugged. "" when the path isn't one of those.
func GuardFor(repository string) string {
	p := LocalRepoPath(repository)
	if p == "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	n := 0
	switch {
	case parts[0] == "mnt" && len(parts) >= 2:
		n = 2
	case parts[0] == "media" && len(parts) >= 3:
		n = 3
	case parts[0] == "media" && len(parts) == 2:
		n = 2
	case len(parts) >= 4 && parts[0] == "run" && parts[1] == "media":
		n = 4
	default:
		return ""
	}
	return "/" + strings.Join(parts[:n], "/")
}

// OnSystemDisk reports whether a local repository path (or its nearest existing parent) is on
// one of the stack's own filesystems.
func (s *Service) OnSystemDisk(path string) (bool, string) {
	repo := s.Repo.Root
	for p := path; ; p = filepath.Dir(p) {
		var st syscall.Stat_t
		if syscall.Stat(p, &st) == nil {
			on, same := systemDevices(repo)[uint64(st.Dev)]
			return same, on
		}
		if filepath.Dir(p) == p {
			return false, ""
		}
	}
}

// refuseInitOnSystemDisk stops `backup init` from creating a repository on the system disk when
// the destination is clearly meant to be an external disk (/mnt/…, /media/…) that isn't mounted.
func (s *Service) refuseInitOnSystemDisk(cfg Settings) error {
	p := LocalRepoPath(cfg.Repo)
	if p == "" || GuardFor(p) == "" {
		return nil
	}
	if same, on := s.OnSystemDisk(p); same {
		return fmt.Errorf("%s is on the same disk as %s — is the backup disk mounted? Refusing to create the "+
			"repository there.\n%s", p, on, mountHint(GuardFor(p)))
	}
	return nil
}

// DiskMissing is what the Backups page shows instead of a restic error while the guard
// fails ("" when the disk is there, or no guard is set).
func (s *Service) DiskMissing(cfg Settings) string {
	if err := s.RequireMount(cfg); err != nil {
		first, _, _ := strings.Cut(err.Error(), "\n")
		return first
	}
	return ""
}

// FollowGuard works out the guard after the destination moves from oldRepo to newRepo. A guard
// that's empty, or that was derived from the old destination, follows the new one; a guard the
// admin set to something else is theirs and stays. It reports whether the guard changed.
func FollowGuard(guard, oldRepo, newRepo string) (string, bool) {
	if guard != "" && guard != GuardFor(oldRepo) {
		return guard, false
	}
	g := GuardFor(newRepo)
	return g, g != guard
}

// GuardMismatch warns when a local repository isn't on the guarded disk at all — the guard would
// then protect nothing.
func GuardMismatch(cfg Settings) string {
	p := LocalRepoPath(cfg.Repo)
	if cfg.RequireMount == "" || p == "" {
		return ""
	}
	g := filepath.Clean(cfg.RequireMount)
	if p != g && !strings.HasPrefix(p, g+"/") {
		return fmt.Sprintf("the destination %s isn't on the guarded disk %s, so the guard doesn't protect it", p, g)
	}
	return ""
}
