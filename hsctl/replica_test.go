package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCfgReplicaRoundTrip(t *testing.T) {
	repo := t.TempDir()
	in := backupCfg{Repo: "/mnt/backup/restic", Retention: defaultRetention,
		ReplicaRepo: "b2:bucket:homeserver"}
	if err := in.save(repo); err != nil {
		t.Fatal(err)
	}
	out := loadBackupCfg(repo)
	if out.ReplicaRepo != in.ReplicaRepo || out.Repo != in.Repo {
		t.Errorf("round-trip mismatch: %+v", out)
	}
	// Clearing the replica must survive a save/load too.
	in.ReplicaRepo = ""
	if err := in.save(repo); err != nil {
		t.Fatal(err)
	}
	if out := loadBackupCfg(repo); out.ReplicaRepo != "" {
		t.Errorf("cleared replica came back: %+v", out)
	}
}

func TestBackupEnvIncludesCreds(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, backupEnvFile),
		[]byte("B2_ACCOUNT_ID=abc\nB2_ACCOUNT_KEY=def\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := strings.Join(backupEnv(repo, "b2:bucket:x"), "\n")
	for _, want := range []string{"RESTIC_REPOSITORY=b2:bucket:x", "B2_ACCOUNT_ID=abc", "B2_ACCOUNT_KEY=def"} {
		if !strings.Contains(env, want) {
			t.Errorf("backupEnv missing %q", want)
		}
	}
	// No creds file at all must still work.
	if env := backupEnv(t.TempDir(), "/tmp/x"); len(env) == 0 {
		t.Error("backupEnv without creds file came back empty")
	}
}

// TestReplicateEndToEnd proves `backup replicate` for real with two local restic repos:
// snapshot data into a primary, replicate (which must create the replica repo on first
// run), and check the snapshot arrived. Skips when restic isn't installed.
func TestReplicateEndToEnd(t *testing.T) {
	if !resticInstalled() {
		t.Skip("restic not installed")
	}
	repo := t.TempDir()
	if err := writeFile0600(filepath.Join(repo, resticPassFile), "test-password\n"); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(repo, "data")
	if err := os.MkdirAll(data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "proof.txt"), []byte("replicate me"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := backupCfg{
		Repo:        filepath.Join(repo, "primary"),
		ReplicaRepo: filepath.Join(repo, "replica"),
	}
	if err := resticAt(repo, cfg.Repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := resticAt(repo, cfg.Repo, "backup", "-q", data); err != nil {
		t.Fatal(err)
	}
	if err := runBackupReplicate(repo, cfg); err != nil {
		t.Fatalf("replicate: %v", err)
	}
	out, err := resticOutput(repo, backupCfg{Repo: cfg.ReplicaRepo}, "snapshots")
	if err != nil {
		t.Fatalf("replica snapshots: %v\n%s", err, out)
	}
	if !strings.Contains(out, "data") || !strings.Contains(out, "1 snapshots") {
		t.Errorf("replica doesn't hold the snapshot:\n%s", out)
	}
	// Second run must be a clean no-op copy, not an error.
	if err := runBackupReplicate(repo, cfg); err != nil {
		t.Errorf("re-replicate: %v", err)
	}
}

func TestReplicateUnconfigured(t *testing.T) {
	err := runBackupReplicate(t.TempDir(), backupCfg{Repo: "/x"})
	if err == nil || !strings.Contains(err.Error(), "no off-site replica configured") {
		t.Errorf("want a configure-me error, got: %v", err)
	}
}
