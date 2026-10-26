// Package images knows the stack's image pins — each compose file's `image: ${VAR:-default}`,
// resolved against the app's .env — and checks that every pin runs on the platforms homeserver
// supports.
//
// homeserver runs on 64-bit hosts only — hsctl ships for linux/amd64 and linux/arm64 — so a tag
// that skips one of those doesn't fail here: it fails at `docker compose pull` on someone's
// Raspberry Pi, long after the pin was merged. The platform check asks each registry what a tag
// actually publishes, reading the manifest index over HTTP with no Docker and no pull, so it works
// on a machine that has never run the stack.
package images

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
)

// SupportedPlatforms is what a pin has to cover: every platform hsctl is published for (see
// DIST_PLATFORMS in hsctl/Makefile). Keep the two lists in step.
var SupportedPlatforms = []string{"linux/amd64", "linux/arm64"}

// ErrBadPin marks a probe failure that is the PIN's fault — the tag was deleted, or the
// repository was renamed or made private — as opposed to the network's. Pin faults fail the
// check; network faults are reported and tolerated.
var ErrBadPin = errors.New("bad pin")

// Pin is one image pinned by a compose file. Pins are declared as `image: ${VAR:-default}`:
// Default is the repo's pin (tracked, frozen), and Image is what compose will actually run — the
// service folder's gitignored .env can override Var with a locally applied update
// (`hsctl updates --apply`), which is how updates land without ever dirtying a tracked file.
type Pin struct {
	File    string // repo-relative, e.g. "nextcloud/docker-compose.yml"
	Var     string // interpolation variable, e.g. NEXTCLOUD_IMAGE ("" for a bare pin)
	Default string // the ref the compose file falls back to
	Image   string // effective ref: the .env override when set, else Default
}

// ParseComposeImage splits a compose `image:` line (optionally quoted) into its interpolation
// variable and default ref. A bare `image: ref` line yields varName "" and the ref as def; ok is
// false when the line isn't an image line at all. This is THE definition of what counts as a pin:
// the platform check and `updates --apply` both use it, so a pin one accepts can't be invisible
// to the other.
func ParseComposeImage(line string) (varName, def string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), "image:")
	if !found {
		return "", "", false
	}
	ref := strings.Trim(strings.TrimSpace(rest), `"'`)
	if inner, isVar := strings.CutPrefix(ref, "${"); isVar && strings.HasSuffix(inner, "}") {
		name, dflt, _ := strings.Cut(strings.TrimSuffix(inner, "}"), ":-")
		return name, dflt, true
	}
	return "", ref, true
}

// EffectiveRef resolves a parsed pin against the service folder's env: the override when the
// variable is set there (compose interpolates from the same .env), else the default.
func EffectiveRef(varName, def string, env map[string]string) string {
	if varName != "" && env[varName] != "" {
		return env[varName]
	}
	return def
}

// Registry is what the platform check asks a registry.
type Registry interface {
	Get(url, accept string) (*http.Response, error)
}

// Checker checks one repo's pins.
type Checker struct {
	Repo     paths.Repo
	Registry Registry
	Out, Err io.Writer
}

// New returns the pin checker for repo.
func New(repo paths.Repo, reg Registry) *Checker {
	return &Checker{Repo: repo, Registry: reg, Out: os.Stdout, Err: os.Stderr}
}

// Pins lists every image pinned by a service's compose file, in path order, resolved against that
// service's .env override (if any). It reads the files rather than asking Docker, so it reports
// what the repo + local overrides declare even when nothing is running.
func (c *Checker) Pins() ([]Pin, error) {
	files, err := filepath.Glob(c.Repo.Path("*", "docker-compose.yml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var pins []Pin
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(c.Repo.Root, f)
		if err != nil {
			rel = f
		}
		env, err := envfile.Read(filepath.Join(filepath.Dir(f), ".env"))
		if err != nil {
			env = nil // no .env (or unreadable) — the compose defaults apply
		}
		for _, line := range strings.Split(string(data), "\n") {
			varName, def, ok := ParseComposeImage(line)
			if !ok {
				continue
			}
			if ref := EffectiveRef(varName, def, env); ref != "" {
				pins = append(pins, Pin{File: rel, Var: varName, Default: def, Image: ref})
			}
		}
	}
	return pins, nil
}

// manifestAccept asks for both index media types (so a multi-platform tag comes back as a list)
// and both single-manifest types (so a single-arch tag answers instead of 404ing).
const manifestAccept = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// ParsePlatforms pulls the runnable platforms out of a manifest index. A bare manifest (valid
// JSON with no "manifests" key) is single-architecture by definition and yields none; a body that
// isn't JSON at all is an error — it means something other than a registry answered (a proxy or
// captive portal), not that the tag publishes nothing. Attestation entries carry os/architecture
// "unknown" and aren't images, so they're skipped; the arm64 variant (v8) is dropped because
// arm64 and arm64/v8 are the same target.
func ParsePlatforms(body []byte) ([]string, error) {
	var doc struct {
		Manifests []struct {
			Platform struct {
				OS   string `json:"os"`
				Arch string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("response isn't a manifest (a proxy or captive portal answered?)")
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range doc.Manifests {
		p := m.Platform
		if p.OS == "" || p.OS == "unknown" || p.Arch == "" || p.Arch == "unknown" {
			continue
		}
		if key := p.OS + "/" + p.Arch; !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Platforms reports the platforms a pinned tag publishes. An error wrapping ErrBadPin means the
// pin itself is broken; any other error means we couldn't get an answer (offline, rate-limited, a
// middlebox in the way).
func (c *Checker) Platforms(image string) ([]string, error) {
	host, repo, tag := registry.SplitRef(image)
	resp, err := c.Registry.Get(registry.Base(host)+"/v2/"+repo+"/manifests/"+tag, manifestAccept)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: tag %s is not in the registry any more", ErrBadPin, tag)
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w: registry denied access (%s) — repository renamed or made private?", ErrBadPin, resp.Status)
	default:
		return nil, fmt.Errorf("registry returned %s", resp.Status)
	}
	// An index is a few KB; the cap is only there so a hostile or broken registry can't stream
	// unbounded data into memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ParsePlatforms(body)
}

// MissingPlatforms returns the wanted platforms a tag doesn't publish.
func MissingPlatforms(want, got []string) []string {
	have := map[string]bool{}
	for _, p := range got {
		have[p] = true
	}
	var missing []string
	for _, p := range want {
		if !have[p] {
			missing = append(missing, p)
		}
	}
	return missing
}

// Check checks every pinned image against its registry. Failures split by fault: a tag that's
// missing a platform, has vanished, or is denied to us is the pin's problem and fails the check.
// A registry we couldn't get an answer from is reported and tolerated — Docker Hub rate-limits
// anonymous callers per IP, and misreporting network luck as a broken stack would erode trust in
// the dashboard card — unless NO pin could be checked, in which case nothing was verified and a
// green exit would be a lie.
func (c *Checker) Check(want []string) error {
	pins, err := c.Pins()
	if err != nil {
		return err
	}
	if len(pins) == 0 {
		return fmt.Errorf("no docker-compose.yml files found under %s", c.Repo.Root)
	}

	fmt.Fprintf(c.Out, "checking %d pinned image(s) cover %s...\n\n", len(pins), strings.Join(want, " + "))
	var bad []string
	checked, unreachable := 0, 0
	for _, p := range pins {
		got, err := c.Platforms(p.Image)
		switch {
		case errors.Is(err, ErrBadPin):
			fmt.Fprintf(c.Out, "  %-12s %-46s %v\n", "BROKEN", p.Image, err)
			bad = append(bad, fmt.Sprintf("%s: %s — %v", p.File, p.Image, err))
		case err != nil:
			fmt.Fprintf(c.Out, "  %-12s %-46s %v\n", "UNREACHABLE", p.Image, err)
			unreachable++
		default:
			checked++
			if missing := MissingPlatforms(want, got); len(missing) > 0 {
				fmt.Fprintf(c.Out, "  %-12s %-46s publishes %s\n", "MISSING", p.Image, strings.Join(got, ", "))
				bad = append(bad, fmt.Sprintf("%s: %s — no %s", p.File, p.Image, strings.Join(missing, ", ")))
			} else {
				fmt.Fprintf(c.Out, "  %-12s %-46s %s\n", "ok", p.Image, strings.Join(got, ", "))
			}
		}
	}

	if len(bad) > 0 {
		fmt.Fprintf(c.Err, "\nthese pins can't run on every host homeserver supports:\n")
		for _, b := range bad {
			fmt.Fprintf(c.Err, "  %s\n", b)
		}
		return fmt.Errorf("%d image(s) failed — pin a tag the upstream publishes for %s, or drop the app",
			len(bad), strings.Join(want, " + "))
	}
	if checked == 0 {
		return fmt.Errorf("none of the %d pins could be checked (registries unreachable or rate-limited) — nothing was verified, re-run to retry", len(pins))
	}
	if unreachable > 0 {
		fmt.Fprintf(c.Out, "\n%d image(s) couldn't be checked (registry unreachable or rate-limited) — re-run to retry.\n", unreachable)
		return nil
	}
	fmt.Fprintf(c.Out, "\nevery image publishes %s.\n", strings.Join(want, " and "))
	// Answer the question the dashboard card is really asked — "will this run on MY server?" — by
	// confirming the machine we're on is itself in the covered set.
	if host := runtime.GOOS + "/" + runtime.GOARCH; slices.Contains(want, host) {
		fmt.Fprintf(c.Out, "this machine is %s — every app runs here.\n", host)
	}
	return nil
}
