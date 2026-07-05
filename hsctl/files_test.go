package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileAtomic checks the atomic secret writer: content and mode are correct, an existing
// file is replaced (not appended), and no temp file is left behind.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	if err := writeFile0600(path, "SECRET=one\n"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "SECRET=one\n" {
		t.Fatalf("content = %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	// Overwrite: the new content fully replaces the old (rename, not append).
	if err := writeFile0600(path, "SECRET=two\n"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "SECRET=two\n" {
		t.Fatalf("after overwrite content = %q, want replaced", b)
	}

	// 0644 helper honours its mode.
	pub := filepath.Join(dir, "public.txt")
	if err := writeFile0644(pub, "x"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(pub); fi.Mode().Perm() != 0644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}

	// No temp files left behind in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hsctl-tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
