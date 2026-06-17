package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
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

// Argon2id parameters for hashing the Vaultwarden admin token. The verifier reads the
// params embedded in the PHC string, so these only need to be sane + secure (this mirrors
// the shape of Vaultwarden's own `vaultwarden hash`).
const (
	argonTime    = 3
	argonMemKiB  = 65540
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// argon2idPHC returns a PHC-format Argon2id hash of token, e.g.
//
//	$argon2id$v=19$m=65540,t=3,p=4$<salt>$<hash>
//
// Vaultwarden's ADMIN_TOKEN should be set to this rather than the plaintext, so the raw
// token is never stored on disk. Verified accepted by vaultwarden 1.32.x–1.36.x.
func argon2idPHC(token string) string {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	h := argon2.IDKey([]byte(token), salt, argonTime, argonMemKiB, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemKiB, argonTime, argonThreads, enc(salt), enc(h))
}

// escapeDollarsForCompose doubles every '$' so docker-compose variable interpolation
// (${VAR}) passes the literal value through to the container. An Argon2 PHC string is full
// of '$'; without this, compose eats them and the token reaching the container is corrupted.
func escapeDollarsForCompose(s string) string { return strings.ReplaceAll(s, "$", "$$") }

// setEnvKey rewrites key=value in a simple KEY=VALUE env file, preserving everything else
// (other keys, comments, order). Appends the key if it isn't present.
func setEnvKey(path, key, value string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, key+"=") {
			lines[i], found = key+"="+value, true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	return writeFile0600(path, strings.Join(lines, "\n"))
}

func writeFile0600(path, content string) error { return os.WriteFile(path, []byte(content), 0600) }
func writeFile0644(path, content string) error { return os.WriteFile(path, []byte(content), 0644) }

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
