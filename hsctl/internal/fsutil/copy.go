package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// WipeDirContents removes everything inside dir but keeps dir itself (it's a mountpoint).
func WipeDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// CopyContents copies the contents of src into dst, preserving ownership, modes and links (cp -a).
func CopyContents(src, dst string, stderr io.Writer) error {
	c := exec.Command("cp", "-a", src+"/.", dst+"/")
	c.Stderr = stderr
	return c.Run()
}

// CopyFile copies a single file src -> dst, preserving ownership and mode (cp -a).
func CopyFile(src, dst string, stderr io.Writer) error {
	c := exec.Command("cp", "-a", src, dst)
	c.Stderr = stderr
	return c.Run()
}

// SHA256File returns the hex SHA-256 of a file's contents.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
