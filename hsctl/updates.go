package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Update checker, two complementary probes per image. The digest probe compares
// the local image digest against the registry's digest for the same tag — it
// catches rebuilt floating tags (caddy:2.8-alpine) but is blind to pinned tags,
// because vaultwarden/server:1.36.0 never changes when 1.37.0 ships. The release
// probe covers that gap: it lists the registry's tags and looks for one with the
// same shape as the pin (same dotted-number count, same variant suffix) but a
// higher version. Read-only — updating stays the deliberate flow (test in the
// sandbox, then edit the tag / compose pull + up).

// imageStatus is one image's check result.
type imageStatus struct {
	Container string
	Image     string
	State     string // "up to date", "UPDATE available", "NEWER RELEASE ...", or an error note
	Stale     bool
	Newer     string // release-probe result: a newer registry tag, or ""
}

// parseRemoteDigest pulls the manifest digest out of `docker buildx imagetools
// inspect` output ("Digest: sha256:...").
func parseRemoteDigest(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "Digest:" && strings.HasPrefix(f[1], "sha256:") {
			return f[1]
		}
	}
	return ""
}

// parseLocalDigests extracts the digest set from an image's RepoDigests
// ("caddy@sha256:aaa,docker.io/library/caddy@sha256:aaa" -> the sha256 parts).
func parseLocalDigests(joined string) []string {
	var out []string
	for _, rd := range strings.Split(joined, ",") {
		if _, d, ok := strings.Cut(rd, "@"); ok && strings.HasPrefix(d, "sha256:") {
			out = append(out, d)
		}
	}
	return out
}

// checkImage compares one image's local digests to the remote digest for its
// tag; newer is the release-probe result (a newer registry tag, or ""), which
// outranks the digest verdict because a rebuilt old pin is still an old pin.
func checkImage(container, image, localJoined, remoteOut, newer string) imageStatus {
	st := imageStatus{Container: container, Image: image, Newer: newer}
	if newer != "" {
		_, _, tag := splitImageRef(image)
		st.State = fmt.Sprintf("NEWER RELEASE %s (pinned to %s)", newer, tag)
		st.Stale = true
		return st
	}
	local := parseLocalDigests(localJoined)
	remote := parseRemoteDigest(remoteOut)
	switch {
	case len(local) == 0:
		st.State = "no local digest (built locally or never pulled) — can't compare"
	case remote == "":
		st.State = "couldn't read the registry digest — offline, rate-limited, or a private image"
	default:
		st.State = "up to date"
		for _, d := range local {
			if d == remote {
				return st
			}
		}
		st.State, st.Stale = "UPDATE available", true
	}
	return st
}

// splitImageRef breaks an image reference into registry host, repository and tag
// ("vaultwarden/server:1.36.0" -> docker.io, vaultwarden/server, 1.36.0;
// "nginx:1.27-alpine" -> docker.io, library/nginx, 1.27-alpine).
func splitImageRef(image string) (host, repo, tag string) {
	if ref, _, ok := strings.Cut(image, "@"); ok {
		image = ref
	}
	host, rest := "docker.io", image
	if first, _, ok := strings.Cut(image, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, rest = first, image[len(first)+1:]
	}
	repo, tag = rest, "latest"
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		repo, tag = rest[:i], rest[i+1:]
	}
	if host == "docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return host, repo, tag
}

// registryBase maps a registry host to its v2 API base URL. Docker Hub's API
// lives on registry-1.docker.io, not docker.io.
func registryBase(host string) string {
	if host == "docker.io" {
		return "https://registry-1.docker.io"
	}
	return "https://" + host
}

var registryHTTP = &http.Client{Timeout: 20 * time.Second}

// bearerToken performs the anonymous token dance a v2 registry asks for via
// WWW-Authenticate: Bearer realm="...",service="...",scope="...".
func bearerToken(wwwAuth string) string {
	fields := map[string]string{}
	for _, kv := range regexp.MustCompile(`(\w+)="([^"]*)"`).FindAllStringSubmatch(wwwAuth, -1) {
		fields[kv[1]] = kv[2]
	}
	realm := fields["realm"]
	if !strings.HasPrefix(wwwAuth, "Bearer ") || realm == "" {
		return ""
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope"} {
		if fields[k] != "" {
			q.Set(k, fields[k])
		}
	}
	resp, err := registryHTTP.Get(realm + "?" + q.Encode())
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct{ Token string `json:"token"` }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return ""
	}
	return body.Token
}

// registryTags lists a repository's tags from a v2 registry (base is e.g.
// https://registry-1.docker.io), following anonymous bearer auth and
// Link-header pagination.
func registryTags(base, repo string) ([]string, error) {
	next, token := base+"/v2/"+repo+"/tags/list?n=1000", ""
	var tags []string
	for page := 0; next != "" && page < 50; page++ {
		req, err := http.NewRequest("GET", next, nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := registryHTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && token == "" {
			auth := resp.Header.Get("Www-Authenticate")
			resp.Body.Close()
			if token = bearerToken(auth); token == "" {
				return nil, fmt.Errorf("registry auth failed for %s", repo)
			}
			page-- // retry the same URL with the token
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("registry returned %s for %s", resp.Status, repo)
		}
		var body struct{ Tags []string `json:"tags"` }
		err = json.NewDecoder(resp.Body).Decode(&body)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		tags = append(tags, body.Tags...)
		next = ""
		if m := regexp.MustCompile(`<([^>]+)>;\s*rel="next"`).FindStringSubmatch(link); m != nil {
			next = m[1]
			if strings.HasPrefix(next, "/") {
				next = base + next
			}
		}
	}
	return tags, nil
}

// buildHashRE matches a trailing "-<git short/long hash>" build suffix
// (it-tools pins like 2024.10.22-7ca5933). Requiring a digit keeps ordinary
// words that happen to be hex letters (e.g. "-decade") from matching.
var buildHashRE = regexp.MustCompile(`-[0-9a-f]*[0-9][0-9a-f]*$`)

// parseTagVersion splits a version-shaped tag into its dotted numbers and the
// variant suffix: "1.27-alpine" -> [1 27], "-alpine"; "2024.10.22-7ca5933" ->
// [2024 10 22], "" (build hash stripped). ok is false for non-version tags
// like "latest" or "apache".
func parseTagVersion(tag string) (nums []int, suffix string, ok bool) {
	if m := buildHashRE.FindString(tag); m != "" && len(m) >= 7 {
		tag = tag[:len(tag)-len(m)]
	}
	rest := tag
	for {
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			return nil, "", false
		}
		n, err := strconv.Atoi(rest[:i])
		if err != nil {
			return nil, "", false
		}
		nums, rest = append(nums, n), rest[i:]
		if !strings.HasPrefix(rest, ".") {
			return nums, rest, true
		}
		rest = rest[1:]
	}
}

// newerVersionTag returns the highest tag among candidates that has the same
// shape as current — same count of dotted numbers and the same variant suffix,
// so nginx:1.27-alpine only ever suggests 1.xx-alpine, never 1.29.1 or plain
// 1.29 — but a higher version. "" when nothing newer is published.
func newerVersionTag(current string, candidates []string) string {
	curNums, curSuffix, ok := parseTagVersion(current)
	if !ok {
		return ""
	}
	best, bestNums := "", curNums
	for _, c := range candidates {
		nums, suffix, ok := parseTagVersion(c)
		if !ok || suffix != curSuffix || len(nums) != len(curNums) {
			continue
		}
		for i := range nums {
			if nums[i] != bestNums[i] {
				if nums[i] > bestNums[i] {
					best, bestNums = c, nums
				}
				break
			}
		}
	}
	return best
}

// newerRelease checks the image's registry for a release tag newer than its
// pinned tag. "" when the tag isn't version-shaped, the registry is
// unreachable, or the pin is already the newest of its shape.
func newerRelease(image string) string {
	host, repo, tag := splitImageRef(image)
	if _, _, ok := parseTagVersion(tag); !ok {
		return ""
	}
	tags, err := registryTags(registryBase(host), repo)
	if err != nil {
		return ""
	}
	return newerVersionTag(tag, tags)
}

// updateTargets lists the stack's real container names. stackContainers entries are
// name FILTERS (docker matches substrings, so "nextcloud" covers nextcloud-app/-db/
// -redis) — this resolves them the same way the status views do.
func updateTargets(repo string) ([]string, error) {
	args := []string{"ps", "-a", "--format", "{{.Names}}"}
	for _, n := range stackContainers {
		args = append(args, "--filter", "name="+n)
	}
	out, err := dockerOut(repo, args...)
	if err != nil {
		return nil, fmt.Errorf("listing containers (is docker up?): %w", err)
	}
	return strings.Fields(out), nil
}

// serviceDirFor maps a container name to its compose service directory
// (nextcloud-db -> nextcloud, stirling-pdf -> stirling; the rest match).
func serviceDirFor(container string) string {
	switch {
	case strings.HasPrefix(container, "nextcloud"):
		return "nextcloud"
	case container == "stirling-pdf":
		return "stirling"
	}
	return container
}

// retag swaps an image reference's tag: retag("vaultwarden/server:1.36.0",
// "1.37.0") -> "vaultwarden/server:1.37.0". Any digest suffix is dropped.
func retag(image, newTag string) string {
	if ref, _, ok := strings.Cut(image, "@"); ok {
		image = ref
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image + ":" + newTag
}

// isMajorJump reports whether moving cur -> next changes the leading version
// number — postgres 16 -> 18 or nextcloud 30 -> 34, which need their own
// upgrade paths, unlike caddy 2.8 -> 2.11. Calendar versions (pihole 2025.x ->
// 2026.x) roll the leading number routinely, so they're exempt.
func isMajorJump(cur, next string) bool {
	a, _, okA := parseTagVersion(cur)
	b, _, okB := parseTagVersion(next)
	return okA && okB && a[0] != b[0] && a[0] < 2000
}

// bumpImageTag rewrites the compose file's `image: oldRef` line(s) to newRef.
// Errors if no line matches — the file drifted from the running container, and
// guessing which line to edit could repin the wrong service.
func bumpImageTag(text, oldRef, newRef string) (string, error) {
	lines := strings.Split(text, "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "image:") && strings.TrimSpace(strings.TrimPrefix(trimmed, "image:")) == oldRef {
			lines[i] = strings.Replace(line, oldRef, newRef, 1)
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("no `image: %s` line found — the compose file has drifted from the running container; edit it by hand", oldRef)
	}
	return strings.Join(lines, "\n"), nil
}

// cmdUpdates checks every stack container's image against its registry tag and
// prints a report. Read-only unless apply names containers (or "all") to update.
func cmdUpdates(apply []string, yes bool) error {
	repo := repoDir()
	if _, err := dockerOut(repo, "buildx", "version"); err != nil {
		return fmt.Errorf("docker buildx is needed to read registry digests — install it:\n" +
			"  sudo apt-get install -y docker-buildx-plugin")
	}
	targets, err := updateTargets(repo)
	if err != nil {
		return err
	}
	var stale int
	var statuses []imageStatus
	fmt.Printf("checking %d containers against their registries...\n\n", len(targets))
	for _, name := range targets {
		image := containerImage(repo, name)
		if image == "" {
			fmt.Printf("%-16s (container not created — skipped)\n", name)
			continue
		}
		localJoined, _ := dockerOut(repo, "image", "inspect", "--format", "{{join .RepoDigests \",\"}}", image)
		remoteOut, _ := dockerOut(repo, "buildx", "imagetools", "inspect", image)
		st := checkImage(name, image, localJoined, remoteOut, newerRelease(image))
		statuses = append(statuses, st)
		mark := "  "
		if st.Stale {
			mark = "! "
			stale++
		}
		fmt.Printf("%s%-16s %-42s %s\n", mark, st.Container, st.Image, st.State)
	}
	if len(apply) > 0 {
		return applyUpdates(repo, statuses, apply, yes)
	}
	if stale == 0 {
		fmt.Println("\neverything is up to date.")
		return nil
	}
	fmt.Printf("\n%d image(s) have an update. To apply (after trying it in the sandbox — see README):\n", stale)
	fmt.Println("  hsctl updates --apply all            # routine updates only")
	fmt.Println("  hsctl updates --apply <container>    # one app, majors included")
	return nil
}

// applyUpdates performs the chosen updates. For a NEWER RELEASE it rewrites the
// pin in the service's docker-compose.yml first; either way the service is then
// compose pull + up -d. "all" selects every stale image but skips major jumps —
// postgres or Nextcloud majors need their own upgrade paths, so those must be
// named explicitly (and confirmed) one at a time.
func applyUpdates(repo string, statuses []imageStatus, targets []string, yes bool) error {
	var chosen []imageStatus
	if len(targets) == 1 && targets[0] == "all" {
		for _, st := range statuses {
			if !st.Stale {
				continue
			}
			if tag := imageTag(st.Image); st.Newer != "" && isMajorJump(tag, st.Newer) {
				fmt.Printf("\nskipping %s: %s -> %s is a major upgrade — test it in the sandbox, then run:\n  hsctl updates --apply %s\n",
					st.Container, tag, st.Newer, st.Container)
				continue
			}
			chosen = append(chosen, st)
		}
	} else {
		byName := map[string]imageStatus{}
		for _, st := range statuses {
			byName[st.Container] = st
		}
		for _, t := range targets {
			st, ok := byName[t]
			if !ok {
				return fmt.Errorf("no stack container named %q (see the list above)", t)
			}
			if !st.Stale {
				fmt.Printf("nothing to apply for %s (%s)\n", t, st.State)
				continue
			}
			chosen = append(chosen, st)
		}
	}
	if len(chosen) == 0 {
		fmt.Println("\nnothing to apply.")
		return nil
	}

	fmt.Println("\nwill apply:")
	for _, st := range chosen {
		if st.Newer == "" {
			fmt.Printf("  %-16s repull %s (its tag was rebuilt upstream)\n", st.Container, st.Image)
			continue
		}
		tag := imageTag(st.Image)
		fmt.Printf("  %-16s %s -> %s (edits %s/docker-compose.yml)\n", st.Container, tag, st.Newer, serviceDirFor(st.Container))
		if isMajorJump(tag, st.Newer) {
			fmt.Printf("  %-16s ^ MAJOR upgrade — make sure you've tested it in the sandbox and have a fresh backup\n", "")
		}
	}
	if !yes {
		if !isTTY() {
			return fmt.Errorf("no terminal to confirm on — re-run with --yes")
		}
		if !askYN("apply now?", false) {
			return fmt.Errorf("aborted — nothing was changed")
		}
	}

	for _, st := range chosen {
		if st.Newer == "" {
			continue
		}
		path := filepath.Join(repo, serviceDirFor(st.Container), "docker-compose.yml")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		edited, err := bumpImageTag(string(data), st.Image, retag(st.Image, st.Newer))
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
			return err
		}
		fmt.Printf("pinned %s to %s\n", path, retag(st.Image, st.Newer))
	}

	done := map[string]bool{}
	for _, st := range chosen {
		svc := serviceDirFor(st.Container)
		if done[svc] {
			continue
		}
		done[svc] = true
		fmt.Printf("\n== updating %s ==\n", svc)
		dir := filepath.Join(repo, svc)
		if err := dockerRun(dir, "compose", "pull"); err != nil {
			return fmt.Errorf("%s: pull: %w", svc, err)
		}
		if err := dockerRun(dir, "compose", "up", "-d"); err != nil {
			return fmt.Errorf("%s: up: %w", svc, err)
		}
	}
	fmt.Println("\ndone — open the apps to confirm they still work (a Check will now show them up to date).")
	return nil
}

// imageTag returns just the tag of an image reference.
func imageTag(image string) string {
	_, _, tag := splitImageRef(image)
	return tag
}
