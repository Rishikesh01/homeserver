package main

import (
	"crypto/rand"
	"os"
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

func writeFile0600(path, content string) error { return os.WriteFile(path, []byte(content), 0600) }
func writeFile0644(path, content string) error { return os.WriteFile(path, []byte(content), 0644) }

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
