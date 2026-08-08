package main

import (
	"fmt"
	"strings"
)

// Update checker: compares each stack container's local image digest against the
// registry's current digest for the same tag, so "is there a newer image?" is a
// button instead of a guess. Read-only — updating stays the deliberate flow
// (test in the sandbox, then compose pull + up).

// imageStatus is one image's check result.
type imageStatus struct {
	Container string
	Image     string
	State     string // "up to date", "UPDATE available", or an error note
	Stale     bool
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

// checkImage compares one image's local digests to the remote digest for its tag.
func checkImage(container, image, localJoined, remoteOut string) imageStatus {
	st := imageStatus{Container: container, Image: image}
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

// cmdUpdates checks every stack container's image against its registry tag and
// prints a report. Read-only: it never pulls or restarts anything.
func cmdUpdates() error {
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
	fmt.Printf("checking %d containers against their registries...\n\n", len(targets))
	for _, name := range targets {
		image := containerImage(repo, name)
		if image == "" {
			fmt.Printf("%-16s (container not created — skipped)\n", name)
			continue
		}
		localJoined, _ := dockerOut(repo, "image", "inspect", "--format", "{{join .RepoDigests \",\"}}", image)
		remoteOut, _ := dockerOut(repo, "buildx", "imagetools", "inspect", image)
		st := checkImage(name, image, localJoined, remoteOut)
		mark := "  "
		if st.Stale {
			mark = "! "
			stale++
		}
		fmt.Printf("%s%-16s %-42s %s\n", mark, st.Container, st.Image, st.State)
	}
	if stale == 0 {
		fmt.Println("\neverything is up to date.")
		return nil
	}
	fmt.Printf("\n%d image(s) have an update. To apply one (after trying it in the sandbox — see README):\n", stale)
	fmt.Println("  cd <service> && docker compose pull && docker compose up -d")
	return nil
}
