package main

// Authority state machine + split-brain guard for local<->cloud migration.
//
// Exactly one side may be "live" (authoritative) at a time, or the family edits two diverging
// copies of their passwords/files with no merge path. A durable marker file (migration.state)
// records who is authoritative; cmdUp and backupRun refuse on the home box whenever home is NOT
// the authoritative side. The marker FAILS CLOSED: a present-but-corrupt marker yields
// authority=unknown and refuses, rather than guessing. An absent marker means "never migrated"
// and is treated as home-authoritative, so existing installs are unaffected.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type authority string

const (
	authHome    authority = "home"
	authCloud   authority = "cloud"
	authUnknown authority = "unknown"
)

const (
	migrationStateFile = "migration.state"
	migrationLockFile  = "migration.lock"
	cloudStateFile     = "cloud-state" // present ONLY on a provisioned cloud VM (set by the seed)
)

// migrationState is the durable record of who is authoritative and where the cloud VM is.
type migrationState struct {
	Authority        authority `json:"authority"`
	Phase            string    `json:"phase,omitempty"`    // e.g. "to-cloud:provisioned", "cloud-live"
	Provider         string    `json:"provider,omitempty"` // hetzner | digitalocean | aws | mock
	CloudServerID    string    `json:"cloud_server_id,omitempty"`
	CloudTailnetAddr string    `json:"cloud_tailnet_addr,omitempty"`
	SnapshotID       string    `json:"snapshot_id,omitempty"`  // pinned snapshot for an idempotent move-back
	VMTag            string    `json:"vm_tag,omitempty"`       // per-trip UUID — verify VM identity, not just name
	AuthorityAt      string    `json:"authority_at,omitempty"` // RFC3339; when authority last changed
	LastError        string    `json:"last_error,omitempty"`
}

// loadState reads migration.state. Fail-closed semantics:
//   - absent           => home (never migrated; the common case, existing installs unaffected)
//   - unreadable/corrupt => unknown (something went wrong with a real marker; refuse to guess)
//   - present & valid   => as recorded (authority normalised to a known value)
func loadState(repo string) migrationState {
	b, err := os.ReadFile(filepath.Join(repo, migrationStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return migrationState{Authority: authHome}
	}
	if err != nil {
		return migrationState{Authority: authUnknown, LastError: err.Error()}
	}
	var s migrationState
	if err := json.Unmarshal(b, &s); err != nil {
		return migrationState{Authority: authUnknown, LastError: "corrupt migration.state: " + err.Error()}
	}
	if s.Authority != authHome && s.Authority != authCloud {
		s.Authority = authUnknown
	}
	return s
}

// saveState writes migration.state atomically and durably: write a temp file, fsync it, rename
// over the target, then fsync the directory — so a crash mid-write never leaves a torn marker
// (a torn marker would read back as unknown and wedge the stack).
func saveState(repo string, s migrationState) error {
	if s.AuthorityAt == "" {
		s.AuthorityAt = time.Now().UTC().Format(time.RFC3339)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	path := filepath.Join(repo, migrationStateFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(repo); err == nil { // best-effort dir fsync so the rename is durable
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// withMigrate runs fn while holding an exclusive, non-blocking flock on migration.lock, so two
// concurrent migrations (double-clicked UI, CLI race) cannot both proceed and spawn two VMs.
func withMigrate(repo string, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(repo, migrationLockFile), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another migration is already running (could not lock %s)", migrationLockFile)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// hostRole is this machine's role: "home" normally, "cloud" on a provisioned VM (the seed drops
// a cloud-state marker). It lets one guard mean "refuse unless THIS host is the authoritative
// one": on home, ops need authority==home; on the cloud VM, authority==cloud.
func hostRole(repo string) authority {
	if fileExists(filepath.Join(repo, cloudStateFile)) {
		return authCloud
	}
	return authHome
}

// guardAuthoritative refuses an operation unless this host is the authoritative side. It is the
// single choke point wired into cmdUp and backupRun.
func guardAuthoritative(repo string) error {
	role := hostRole(repo)
	st := loadState(repo)
	if st.Authority == role {
		return nil
	}
	switch {
	case role == authHome && st.Authority == authCloud:
		return fmt.Errorf("refusing: the CLOUD is the live copy right now (provider=%s addr=%s). Starting or "+
			"backing up home would split-brain your data.\n  Bring it home:   hsctl migrate back\n  "+
			"If the cloud is truly gone: hsctl migrate force-home",
			st.Provider, st.CloudTailnetAddr)
	case st.Authority == authUnknown:
		return fmt.Errorf("refusing: migration state is unknown/corrupt (%s).\n  Inspect: hsctl migrate status"+
			"\n  If home is truly authoritative: hsctl migrate force-home", st.LastError)
	default:
		return fmt.Errorf("refusing: this host's role is %q but the authoritative side is %q (inspect: hsctl migrate status)",
			role, st.Authority)
	}
}
