package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoveEnvKeys covers the migration primitive that moves an existing install onto the
// shared Caddy network: it must drop the obsolete keys, keep everything else (values,
// comments, order), be idempotent, and treat a missing file as a no-op.
func TestRemoveEnvKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	orig := "SERVER_IP=192.168.0.150\n" +
		"VAULT_UPSTREAM=host.docker.internal:8082\n" +
		"HOME_UPSTREAM=host.docker.internal:8088\n" +
		"# a comment stays\n" +
		"CLOUD_UPSTREAM=host.docker.internal:8081\n" +
		"VAULT_HTTPS=8443\n"
	if err := os.WriteFile(path, []byte(orig), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeEnvKeys(path, "VAULT_UPSTREAM", "CLOUD_UPSTREAM", "PIHOLE_UPSTREAM")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true when keys are present")
	}
	got, _ := os.ReadFile(path)
	for _, gone := range []string{"VAULT_UPSTREAM", "CLOUD_UPSTREAM"} {
		if strings.Contains(string(got), gone) {
			t.Errorf("%s should have been removed, file is:\n%s", gone, got)
		}
	}
	// The dashboard upstream (still a host process), other settings, and comments must survive.
	for _, keep := range []string{
		"SERVER_IP=192.168.0.150", "HOME_UPSTREAM=host.docker.internal:8088",
		"# a comment stays", "VAULT_HTTPS=8443",
	} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("%q should have been preserved, file is:\n%s", keep, got)
		}
	}

	// Second run is a no-op (nothing left to remove).
	if changed, err := removeEnvKeys(path, "VAULT_UPSTREAM", "CLOUD_UPSTREAM"); err != nil || changed {
		t.Errorf("second run should be a no-op, got changed=%v err=%v", changed, err)
	}
	// Missing file is a silent no-op, not an error.
	if changed, err := removeEnvKeys(filepath.Join(dir, "absent.env"), "X"); err != nil || changed {
		t.Errorf("missing file should be a no-op, got changed=%v err=%v", changed, err)
	}
}
