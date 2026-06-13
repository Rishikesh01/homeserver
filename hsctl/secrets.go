package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const alphanum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// genPassword returns a cryptographically-random alphanumeric string of length n.
func genPassword(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is fatal and unrecoverable
	}
	for i := range b {
		b[i] = alphanum[int(b[i])%len(alphanum)]
	}
	return string(b)
}

// bcryptHash returns a bcrypt hash of pw (via python3, which the host already has
// for the stack). Keeping it out-of-process means hsctl has zero Go dependencies.
func bcryptHash(pw string) (string, error) {
	out, err := exec.Command("python3", "-c",
		"import bcrypt,sys;print(bcrypt.hashpw(sys.argv[1].encode(),bcrypt.gensalt()).decode())",
		pw).Output()
	if err != nil {
		return "", fmt.Errorf("bcrypt (needs python3 'bcrypt' module): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// dollarEscape doubles "$" so docker-compose's env_file interpolation can't mangle
// the bcrypt hash (a single "$" gets eaten; "$$" collapses back to one).
func dollarEscape(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func writeFile0600(path, content string) error { return os.WriteFile(path, []byte(content), 0600) }
func writeFile0644(path, content string) error { return os.WriteFile(path, []byte(content), 0644) }

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
