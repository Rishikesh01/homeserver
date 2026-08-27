package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
// token is never stored on disk. Verified accepted by vaultwarden 1.32.x–1.37.x.
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

// upsertEnvKey is setEnvKey for a file that may not exist yet — some service dirs
// (it-tools, imagetools) have no secrets, and gain their first .env when an image-pin
// override (`hsctl updates --apply`) lands there.
func upsertEnvKey(path, key, value string) error {
	if !fileExists(path) {
		return writeFile0600(path, key+"="+value+"\n")
	}
	return setEnvKey(path, key, value)
}

// removeEnvLinesMatching deletes KEY=VALUE lines where match(key, value) is true, preserving
// everything else (other keys, comments, order). A missing file is a no-op. Reports whether it
// changed anything — used to migrate away from settings no longer used.
func removeEnvLinesMatching(path string, match func(key, value string) bool) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var kept []string
	changed := false
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && match(strings.TrimSpace(k), strings.TrimSpace(v)) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return false, nil
	}
	return true, writeFile0600(path, strings.Join(kept, "\n"))
}

// removeEnvKeys deletes any KEY=... lines for the given keys, whatever their value.
func removeEnvKeys(path string, keys ...string) (bool, error) {
	return removeEnvLinesMatching(path, func(k, _ string) bool { return slices.Contains(keys, k) })
}

// writeFileAtomic writes content to path atomically: it writes a temp file in the SAME
// directory, fsyncs it, then renames it over path. A rename is all-or-nothing on POSIX, so a
// crash or power loss mid-write can never leave a half-written secret file — a truncated .env or
// password is worse than a missing one, since `setup` skips (won't regenerate) a file that
// exists. On any failure the temp file is removed and path is left untouched.
func writeFileAtomic(path, content string, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hsctl-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	// Under sudo, generated files must end up owned by the real user, not root —
	// `sudo hsctl install` is the documented flow, and a root-owned .ui-password or
	// .env would be unreadable to the login user afterwards (`cat`, `hsctl secrets
	// show`). The systemd service runs without SUDO_UID and is unaffected.
	if uid, gid, ok := invokerIDs(); ok {
		if err := tmp.Chown(uid, gid); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	remove = false // renamed into place; nothing to clean up
	return nil
}

func writeFile0600(path, content string) error { return writeFileAtomic(path, content, 0600) }
func writeFile0644(path, content string) error { return writeFileAtomic(path, content, 0644) }

// invokerIDs returns the sudo-invoking user's uid/gid when running as root under
// sudo; ok=false otherwise (plain user, or a root session with no sudo behind it).
func invokerIDs() (uid, gid int, ok bool) {
	if os.Geteuid() != 0 {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return uid, gid, true
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
