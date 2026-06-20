package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStateDefaults(t *testing.T) {
	// Absent marker => home (never migrated; existing installs must still start).
	if st := loadState(t.TempDir()); st.Authority != authHome {
		t.Errorf("absent state should be home, got %q", st.Authority)
	}
	// Corrupt marker => unknown (fail closed; don't guess).
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, migrationStateFile), []byte("{not json"), 0600)
	if st := loadState(repo); st.Authority != authUnknown {
		t.Errorf("corrupt state should be unknown, got %q", st.Authority)
	}
	// Unrecognised authority value => unknown.
	os.WriteFile(filepath.Join(repo, migrationStateFile), []byte(`{"authority":"banana"}`), 0600)
	if st := loadState(repo); st.Authority != authUnknown {
		t.Errorf("bogus authority should normalise to unknown, got %q", st.Authority)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	repo := t.TempDir()
	in := migrationState{Authority: authCloud, Provider: "hetzner", CloudServerID: "42",
		CloudTailnetAddr: "cloudbox", SnapshotID: "abc123", VMTag: "uuid-1"}
	if err := saveState(repo, in); err != nil {
		t.Fatal(err)
	}
	got := loadState(repo)
	if got.Authority != authCloud || got.Provider != "hetzner" || got.CloudServerID != "42" ||
		got.SnapshotID != "abc123" || got.VMTag != "uuid-1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.AuthorityAt == "" {
		t.Error("saveState should stamp AuthorityAt")
	}
	// The temp file must not linger.
	if fileExists(filepath.Join(repo, migrationStateFile+".tmp")) {
		t.Error("temp state file leaked")
	}
}

func TestGuardAuthoritative(t *testing.T) {
	// Home role (no cloud-state marker).
	repo := t.TempDir()
	if err := guardAuthoritative(repo); err != nil { // absent => home => allowed
		t.Errorf("home with no state should be allowed, got: %v", err)
	}
	saveState(repo, migrationState{Authority: authCloud, Provider: "hetzner", CloudTailnetAddr: "cloudbox"})
	if err := guardAuthoritative(repo); err == nil {
		t.Error("home must be refused while the cloud is authoritative")
	}
	os.WriteFile(filepath.Join(repo, migrationStateFile), []byte("garbage"), 0600)
	if err := guardAuthoritative(repo); err == nil {
		t.Error("home must be refused while state is unknown/corrupt")
	}

	// Cloud role (cloud-state marker present, as the seed would drop on the VM).
	cloud := t.TempDir()
	os.WriteFile(filepath.Join(cloud, cloudStateFile), []byte("vm"), 0600)
	saveState(cloud, migrationState{Authority: authCloud})
	if err := guardAuthoritative(cloud); err != nil {
		t.Errorf("cloud VM with authority=cloud should be allowed, got: %v", err)
	}
	saveState(cloud, migrationState{Authority: authHome})
	if err := guardAuthoritative(cloud); err == nil {
		t.Error("cloud VM must be refused when authority=home")
	}
}

func TestWithMigrateFlock(t *testing.T) {
	repo := t.TempDir()
	inner := withMigrate(repo, func() error {
		// A second lock attempt while the first is held must fail (cross-fd flock on Linux).
		return withMigrate(repo, func() error { return nil })
	})
	if inner == nil {
		t.Error("a nested migration should be blocked by the flock")
	}
	// After release, the lock is acquirable again.
	if err := withMigrate(repo, func() error { return nil }); err != nil {
		t.Errorf("lock should be free after release, got: %v", err)
	}
}
