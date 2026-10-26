// Package envfile reads and edits KEY=VALUE files — the per-service .env files, setup.conf and
// backup.conf — the way docker compose reads a .env, and edits them in place so every other line
// (comments, unknown keys, order) survives.
package envfile

import (
	"bufio"
	"os"
	"slices"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
)

// Read parses a KEY=VALUE file. Blank lines and # comments are skipped.
func Read(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = Value(v)
	}
	return m, sc.Err()
}

// Value parses the value half of a KEY=VALUE line the way docker compose's .env parser does, so
// hsctl and compose agree on what a file says: a quoted value is everything between the quotes,
// and an unquoted value ends at an inline comment (a '#' after whitespace). Without the comment
// rule, `SERVER_IP=192.168.1.10   # this host's IP` read as the whole line and got pasted into
// every generated .env.
func Value(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
		return v // unterminated quote: take it literally
	}
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// Set rewrites key=value in an existing file, preserving everything else (other keys, comments,
// order). Appends the key if it isn't present.
func Set(path, key, value string) error {
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
		// Before the file's final newline, so the new key doesn't follow a blank line and the file
		// still ends with a newline.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = append(lines[:n-1], key+"="+value, "")
		} else {
			lines = append(lines, key+"="+value)
		}
	}
	return fsutil.WritePrivate(path, strings.Join(lines, "\n"))
}

// Upsert is Set for a file that may not exist yet — some service dirs (it-tools, imagetools) have
// no secrets, and gain their first .env when an image-pin override (`hsctl updates --apply`) lands.
func Upsert(path, key, value string) error {
	if !fsutil.Exists(path) {
		return fsutil.WritePrivate(path, key+"="+value+"\n")
	}
	return Set(path, key, value)
}

// RemoveMatching deletes KEY=VALUE lines where match(key, value) is true, preserving everything
// else. A missing file is a no-op. Reports whether it changed anything.
func RemoveMatching(path string, match func(key, value string) bool) (bool, error) {
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
	return true, fsutil.WritePrivate(path, strings.Join(kept, "\n"))
}

// RemoveKeys deletes any KEY=... lines for the given keys, whatever their value.
func RemoveKeys(path string, keys ...string) (bool, error) {
	return RemoveMatching(path, func(k, _ string) bool { return slices.Contains(keys, k) })
}
