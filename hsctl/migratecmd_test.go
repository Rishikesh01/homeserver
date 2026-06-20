package main

import (
	"fmt"
	"strings"
	"testing"
)

// fakeSteps records the order of side-effecting steps and can inject a failure at any one of
// them, so the orchestration's sequencing + rollback + idempotency can be tested without
// restic/docker/SSH.
type fakeSteps struct {
	calls          []string
	failAt         string
	existing       bool
	homeSnap       string
	cloudSnap      string
	rehydratedSnap string
}

func (f *fakeSteps) rec(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return fmt.Errorf("injected failure at %s", name)
	}
	return nil
}
func (f *fakeSteps) adopt(name string) (VM, bool, error) {
	f.calls = append(f.calls, "adopt")
	return VM{ID: "vm1", Name: name}, f.existing, nil
}
func (f *fakeSteps) captureHome(tag string) (string, error) {
	return f.homeSnap, f.rec("captureHome")
}
func (f *fakeSteps) provision(spec VMSpec) (VM, error) {
	return VM{ID: "vm1", Name: spec.Name, Tag: spec.Tag}, f.rec("provision")
}
func (f *fakeSteps) seedCloud(VM, string, hostIdentity) error { return f.rec("seedCloud") }
func (f *fakeSteps) probeCloud(VM, hostIdentity) error        { return f.rec("probeCloud") }
func (f *fakeSteps) sealHome() error                          { return f.rec("sealHome") }
func (f *fakeSteps) sealCloud(VM) error                       { return f.rec("sealCloud") }
func (f *fakeSteps) captureCloud(VM) (string, error)          { return f.cloudSnap, f.rec("captureCloud") }
func (f *fakeSteps) rehydrateHome(snap string, _ hostIdentity) error {
	f.rehydratedSnap = snap
	return f.rec("rehydrateHome")
}
func (f *fakeSteps) verifyHome(hostIdentity) error { return f.rec("verifyHome") }
func (f *fakeSteps) destroy(VM) error              { return f.rec("destroy") }

func testMigrator(repo string, f *fakeSteps) *migrator {
	return &migrator{
		repo: repo, cfg: Config{ServerIP: "192.168.0.150"}, cloud: cloudCfg{Provider: "mock"},
		provider: &mockProvider{repo: repo}, steps: f, log: func(string, ...any) {},
	}
}

func has(calls []string, name string) bool {
	for _, c := range calls {
		if c == name {
			return true
		}
	}
	return false
}

func TestToCloudHappyPath(t *testing.T) {
	repo := t.TempDir()
	f := &fakeSteps{homeSnap: "snap1"}
	if err := testMigrator(repo, f).toCloud(); err != nil {
		t.Fatalf("toCloud: %v", err)
	}
	want := "adopt captureHome provision seedCloud probeCloud sealHome"
	if got := strings.Join(f.calls, " "); got != want {
		t.Errorf("call order:\n got %q\nwant %q", got, want)
	}
	st := loadState(repo)
	if st.Authority != authCloud || st.SnapshotID != "snap1" || st.Phase != "cloud-live" {
		t.Errorf("final state: %+v", st)
	}
}

func TestToCloudRefusesWhenVMExists(t *testing.T) {
	repo := t.TempDir()
	f := &fakeSteps{existing: true}
	if err := testMigrator(repo, f).toCloud(); err == nil {
		t.Fatal("expected refusal when a VM already exists")
	}
	if has(f.calls, "captureHome") {
		t.Error("must not capture/seal when refusing on an existing VM")
	}
	if loadState(repo).Authority != authHome {
		t.Error("authority must remain home")
	}
}

func TestToCloudProbeFailRollsBack(t *testing.T) {
	repo := t.TempDir()
	f := &fakeSteps{homeSnap: "snap1", failAt: "probeCloud"}
	if err := testMigrator(repo, f).toCloud(); err == nil {
		t.Fatal("expected probe failure")
	}
	if !has(f.calls, "destroy") {
		t.Error("a failed probe must destroy the VM (rollback)")
	}
	if has(f.calls, "sealHome") {
		t.Error("home must NOT be sealed when the probe failed (invariant: probe before seal)")
	}
	if loadState(repo).Authority != authHome {
		t.Error("authority must remain home after rollback")
	}
}

func TestBackHappyPath(t *testing.T) {
	repo := t.TempDir()
	saveState(repo, migrationState{Authority: authCloud, Provider: "mock", CloudServerID: "vm1",
		CloudTailnetAddr: "cloudbox", SnapshotID: "old", Phase: "cloud-live"})
	f := &fakeSteps{cloudSnap: "snap2"}
	if err := testMigrator(repo, f).back(); err != nil {
		t.Fatalf("back: %v", err)
	}
	want := "sealCloud captureCloud rehydrateHome verifyHome destroy"
	if got := strings.Join(f.calls, " "); got != want {
		t.Errorf("call order:\n got %q\nwant %q", got, want)
	}
	if f.rehydratedSnap != "snap2" {
		t.Errorf("home should be rehydrated from the fresh cloud capture, got %q", f.rehydratedSnap)
	}
	st := loadState(repo)
	if st.Authority != authHome || st.Phase != "" || st.CloudServerID != "" {
		t.Errorf("final state: %+v", st)
	}
}

func TestBackRehydrateFailKeepsCloud(t *testing.T) {
	repo := t.TempDir()
	saveState(repo, migrationState{Authority: authCloud, Provider: "mock", CloudServerID: "vm1", Phase: "cloud-live"})
	f := &fakeSteps{cloudSnap: "snap2", failAt: "rehydrateHome"}
	if err := testMigrator(repo, f).back(); err == nil {
		t.Fatal("expected rehydrate failure")
	}
	if has(f.calls, "destroy") {
		t.Error("must NOT destroy the cloud when home rehydrate failed (invariant: verify home before destroy)")
	}
	st := loadState(repo)
	if st.Authority != authHome || st.Phase != "move-back:captured" {
		t.Errorf("after capture+commit, state should allow an idempotent retry: %+v", st)
	}
}

func TestBackResumesAfterCapture(t *testing.T) {
	repo := t.TempDir()
	// Simulate a crash AFTER the cloud was captured + authority flipped home, BEFORE rehydrate.
	saveState(repo, migrationState{Authority: authHome, Phase: "move-back:captured",
		Provider: "mock", CloudServerID: "vm1", SnapshotID: "pinned"})
	f := &fakeSteps{}
	if err := testMigrator(repo, f).back(); err != nil {
		t.Fatalf("resume back: %v", err)
	}
	if has(f.calls, "sealCloud") || has(f.calls, "captureCloud") {
		t.Error("a resume must NOT re-seal/re-capture the already-sealed cloud (idempotency)")
	}
	if f.rehydratedSnap != "pinned" {
		t.Errorf("resume must rehydrate from the PINNED snapshot, got %q", f.rehydratedSnap)
	}
	if !has(f.calls, "destroy") || loadState(repo).Authority != authHome {
		t.Error("resume should finish: rehydrate -> verify -> destroy")
	}
}
