package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Platform coverage for the images the stack pins. homeserver runs on 64-bit hosts only
// — hsctl ships for linux/amd64 and linux/arm64 — so a tag that skips one of those
// doesn't fail here: it fails at `docker compose pull` on someone's Raspberry Pi, long
// after the pin was merged. `hsctl images` asks each registry what a tag actually
// publishes, reading the manifest index over HTTP with no Docker and no pull, so it
// works on a machine that has never run the stack. CI runs it on every PR.

// supportedPlatforms is what a pin has to cover: every platform we publish hsctl for
// (see DIST_PLATFORMS in hsctl/Makefile). Keep the two lists in step.
var supportedPlatforms = []string{"linux/amd64", "linux/arm64"}

// composePin is one image reference and the compose file that pins it.
type composePin struct {
	File  string // repo-relative, e.g. "nextcloud/docker-compose.yml"
	Image string
}

var composeImageRE = regexp.MustCompile(`(?m)^\s*image:\s*(\S+)`)

// composePins lists every image pinned by a service's compose file, in path order.
// It reads the files rather than asking Docker, so it reports what the repo declares
// even when nothing is running.
func composePins(repo string) ([]composePin, error) {
	files, err := filepath.Glob(filepath.Join(repo, "*", "docker-compose.yml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var pins []composePin
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(repo, f)
		if err != nil {
			rel = f
		}
		for _, m := range composeImageRE.FindAllStringSubmatch(string(data), -1) {
			pins = append(pins, composePin{File: rel, Image: strings.Trim(m[1], `"'`)})
		}
	}
	return pins, nil
}

// registryGet performs a v2 registry GET, following the anonymous bearer-token
// challenge the way registryTags does. accept selects the manifest media types.
func registryGet(url, accept string) (*http.Response, error) {
	get := func(token string) (*http.Response, error) {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return registryHTTP.Do(req)
	}

	resp, err := get("")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	auth := resp.Header.Get("Www-Authenticate")
	resp.Body.Close()
	token := bearerToken(auth)
	if token == "" {
		return nil, fmt.Errorf("registry auth failed")
	}
	return get(token)
}

// manifestAccept asks for both index media types (so a multi-platform tag comes back as
// a list) and both single-manifest types (so a single-arch tag answers instead of 404ing).
const manifestAccept = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// parsePlatforms pulls the runnable platforms out of a manifest index. A bare manifest
// (no "manifests" key) is single-architecture by definition and yields none. Attestation
// entries carry os/architecture "unknown" and aren't images, so they're skipped; the
// arm64 variant (v8) is dropped because arm64 and arm64/v8 are the same target.
func parsePlatforms(body []byte) []string {
	var doc struct {
		Manifests []struct {
			Platform struct {
				OS   string `json:"os"`
				Arch string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil
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
	return out
}

// imagePlatforms reports the platforms a pinned tag publishes.
func imagePlatforms(image string) ([]string, error) {
	host, repo, tag := splitImageRef(image)
	resp, err := registryGet(registryBase(host)+"/v2/"+repo+"/manifests/"+tag, manifestAccept)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("tag %s is not in the registry any more", tag)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned %s", resp.Status)
	}
	// An index is a few KB; the cap is only there so a hostile or broken registry
	// can't stream unbounded data into memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parsePlatforms(body), nil
}

// missingPlatforms returns the wanted platforms a tag doesn't publish.
func missingPlatforms(want, got []string) []string {
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

// cmdImages checks every pinned image against its registry. A tag that's missing a
// platform (or has vanished) is an error; a registry we simply couldn't reach is
// reported and tolerated, because Docker Hub rate-limits anonymous callers per IP and
// CI runners share addresses — failing there would mean red builds that have nothing
// to do with the change under review.
func cmdImages(want []string) error {
	repo := repoDir()
	pins, err := composePins(repo)
	if err != nil {
		return err
	}
	if len(pins) == 0 {
		return fmt.Errorf("no docker-compose.yml files found under %s", repo)
	}

	fmt.Printf("checking %d pinned image(s) cover %s...\n\n", len(pins), strings.Join(want, " + "))
	var bad, unreachable []string
	for _, p := range pins {
		got, err := imagePlatforms(p.Image)
		if err != nil {
			fmt.Printf("  %-12s %-46s %v\n", "UNREACHABLE", p.Image, err)
			unreachable = append(unreachable, fmt.Sprintf("%s: %s — %v", p.File, p.Image, err))
			continue
		}
		if missing := missingPlatforms(want, got); len(missing) > 0 {
			fmt.Printf("  %-12s %-46s publishes %s\n", "MISSING", p.Image, strings.Join(got, ", "))
			bad = append(bad, fmt.Sprintf("%s: %s — no %s", p.File, p.Image, strings.Join(missing, ", ")))
			continue
		}
		fmt.Printf("  %-12s %-46s %s\n", "ok", p.Image, strings.Join(got, ", "))
	}

	if len(bad) > 0 {
		fmt.Fprintf(os.Stderr, "\nthese pins can't run on every host homeserver supports:\n")
		for _, b := range bad {
			fmt.Fprintf(os.Stderr, "  %s\n", b)
		}
		return fmt.Errorf("%d image(s) don't cover %s — pin a tag the upstream builds for both, or drop the app",
			len(bad), strings.Join(want, " + "))
	}
	if len(unreachable) > 0 {
		fmt.Printf("\n%d image(s) couldn't be checked (registry unreachable or rate-limited) — re-run to retry.\n",
			len(unreachable))
		return nil
	}
	fmt.Printf("\nevery image publishes %s.\n", strings.Join(want, " and "))
	return nil
}
