package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const secretsListFile = ".secrets.txt"

// secretsCmd shows or securely deletes the generated-logins file. The plaintext logins
// live ONLY in .secrets.txt (a convenience copy) — the running stack reads from each
// service's .env. The intended flow: `hsctl secrets show` -> save them into Vaultwarden
// -> `hsctl secrets shred`.
func secretsCmd() *cobra.Command {
	s := &cobra.Command{Use: "secrets", Short: "Show or securely delete the generated-logins file (.secrets.txt)"}
	s.AddCommand(
		&cobra.Command{Use: "show", Short: "Print " + secretsListFile + " (copy these into Vaultwarden)",
			Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return secretsShow(repoDir()) }},
		&cobra.Command{Use: "shred", Short: "Overwrite " + secretsListFile + " with random data, then delete it",
			Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return secretsShred(repoDir()) }},
	)
	return s
}

func secretsShow(repo string) error {
	b, err := os.ReadFile(filepath.Join(repo, secretsListFile))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s not found (already shredded, or nothing was generated)", secretsListFile)
		}
		return err
	}
	os.Stdout.Write(b)
	return nil
}

// secretsShred overwrites the file with random bytes (3 passes), fsyncs, then removes it.
//
// Caveat: on SSDs (wear-leveling) and copy-on-write filesystems (btrfs/ZFS), overwriting
// a file's logical blocks does NOT guarantee the physical blocks are erased. Full-disk
// encryption (LUKS) is the real at-rest protection — with it, any remnant is ciphertext.
func secretsShred(repo string) error {
	path := filepath.Join(repo, secretsListFile)
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("already gone:", secretsListFile)
			return nil
		}
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	for pass := 0; pass < 3; pass++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		if _, err := io.CopyN(f, rand.Reader, fi.Size()); err != nil {
			f.Close()
			return err
		}
		f.Sync()
	}
	f.Close()
	if err := os.Remove(path); err != nil {
		return err
	}
	fmt.Printf("shredded %s (3 random-data passes, then removed)\n", secretsListFile)
	fmt.Println("note: with full-disk encryption this is belt-and-suspenders; without it, SSD/CoW")
	fmt.Println("filesystems may still retain remnants — disk encryption is the real protection.")
	return nil
}
