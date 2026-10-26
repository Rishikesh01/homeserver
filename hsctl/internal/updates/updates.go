// Package updates checks the stack's running images against their registries and applies updates.
//
// Two complementary probes per image. The digest probe compares the local image digest against
// the registry's digest for the same tag — it catches a rebuilt tag — but is blind to a new
// release, because vaultwarden/server:1.37.3 never changes when 1.37.4 ships. The release probe
// covers that gap: it lists the registry's tags and looks for one with the same shape as the pin
// (same dotted-number count, same variant suffix) but a higher version. Checking is read-only;
// applying records the new tag in the service's gitignored .env (the compose files declare pins as
// `image: ${VAR:-default}`), so updates never dirty a tracked file.
package updates

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/images"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
)

// Docker is what the checker needs from Docker.
type Docker interface {
	Output(dir string, args ...string) (string, error)
	Run(dir string, args ...string) error
	ContainerImage(name string) string
	Require() error
	ComposeUpArgs(extra ...string) []string
	ComposePullArgs() []string
}

// Registry lists a repository's tags.
type Registry interface {
	Tags(base, repo string) ([]string, error)
}

// Asker confirms an update with the person at the terminal.
type Asker interface {
	CanAsk() bool
	YesNo(prompt string, def bool) bool
}

// Checker checks and applies updates for one repo's stack.
type Checker struct {
	Repo     paths.Repo
	Docker   Docker
	Registry Registry
	// Containers lists the stack's container names (stopped ones included).
	Containers func() ([]string, error)
	Ask        Asker
	Out        io.Writer
}

// New returns the update checker for repo.
func New(repo paths.Repo, dk Docker, reg Registry, containers func() ([]string, error), ask Asker) *Checker {
	return &Checker{Repo: repo, Docker: dk, Registry: reg, Containers: containers, Ask: ask, Out: os.Stdout}
}

// Status is one image's check result.
type Status struct {
	Container string
	Image     string
	State     string // "up to date", "UPDATE available", "NEWER RELEASE ...", or an error note
	Stale     bool
	Newer     string // release-probe result: a newer registry tag, or ""
}

// Major reports whether applying st crosses a major version.
func (st Status) Major() bool {
	return st.Newer != "" && IsMajorJump(registry.Tag(st.Image), st.Newer)
}

// ParseRemoteDigest pulls the manifest digest out of `docker buildx imagetools inspect` output
// ("Digest: sha256:...").
func ParseRemoteDigest(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "Digest:" && strings.HasPrefix(f[1], "sha256:") {
			return f[1]
		}
	}
	return ""
}

// ParseLocalDigests extracts the digest set from an image's RepoDigests
// ("caddy@sha256:aaa,docker.io/library/caddy@sha256:aaa" -> the sha256 parts).
func ParseLocalDigests(joined string) []string {
	var out []string
	for _, rd := range strings.Split(joined, ",") {
		if _, d, ok := strings.Cut(rd, "@"); ok && strings.HasPrefix(d, "sha256:") {
			out = append(out, d)
		}
	}
	return out
}

// Compare decides one image's status from its local digests, the registry's digest for its tag,
// and the release probe's result (a newer tag, or ""), which outranks the digest verdict because
// a rebuilt old pin is still an old pin.
func Compare(container, image, localJoined, remoteOut, newer string) Status {
	st := Status{Container: container, Image: image, Newer: newer}
	if newer != "" {
		st.State = fmt.Sprintf("NEWER RELEASE %s (pinned to %s)", newer, registry.Tag(image))
		st.Stale = true
		return st
	}
	local := ParseLocalDigests(localJoined)
	remote := ParseRemoteDigest(remoteOut)
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

// buildHashRE matches a trailing "-<git short/long hash>" build suffix (it-tools pins like
// 2024.10.22-7ca5933). Requiring a digit keeps ordinary words that happen to be hex letters (e.g.
// "-decade") from matching.
var buildHashRE = regexp.MustCompile(`-[0-9a-f]*[0-9][0-9a-f]*$`)

// ParseTagVersion splits a version-shaped tag into its dotted numbers and the variant suffix:
// "1.31.8-alpine" -> [1 31 8], "-alpine"; "2024.10.22-7ca5933" -> [2024 10 22], "" (build hash
// stripped). ok is false for non-version tags like "latest" or "apache".
func ParseTagVersion(tag string) (nums []int, suffix string, ok bool) {
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

// NewerVersionTag returns the highest tag among candidates that has the same shape as current —
// same count of dotted numbers and the same variant suffix, so nginx:1.31.8-alpine only ever
// suggests 1.x.y-alpine, never 1.32 or a -slim variant — but a higher version. "" when nothing
// newer is published.
func NewerVersionTag(current string, candidates []string) string {
	curNums, curSuffix, ok := ParseTagVersion(current)
	if !ok {
		return ""
	}
	best, bestNums := "", curNums
	for _, c := range candidates {
		nums, suffix, ok := ParseTagVersion(c)
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

// IsMajorJump reports whether moving cur -> next changes the leading version number — postgres
// 18 -> 19 or nextcloud 35 -> 36, which need their own upgrade paths, unlike caddy 2.11 -> 2.12.
// Calendar versions (pihole 2025.x -> 2026.x) roll the leading number routinely, so they're exempt.
func IsMajorJump(cur, next string) bool {
	a, _, okA := ParseTagVersion(cur)
	b, _, okB := ParseTagVersion(next)
	return okA && okB && a[0] != b[0] && a[0] < 2000
}

// PinVarFor finds the compose interpolation variable whose effective image ref (the .env override
// when set, else the compose default) equals ref — the pin an update is about to move. Matching
// uses images.ParseComposeImage, the same definition of a pin the platform check uses, so a pin
// `hsctl images` accepts is always movable here. Errors when nothing resolves to ref (the file
// drifted from the running container, and guessing could repin the wrong service) or when the
// match is a bare pin with no variable to override.
func PinVarFor(composeText string, env map[string]string, ref string) (string, error) {
	for _, line := range strings.Split(composeText, "\n") {
		varName, def, ok := images.ParseComposeImage(line)
		if !ok || images.EffectiveRef(varName, def, env) != ref {
			continue
		}
		if varName == "" {
			return "", fmt.Errorf("`image: %s` is a bare pin without an ${..._IMAGE} variable — edit the compose file by hand", ref)
		}
		return varName, nil
	}
	return "", fmt.Errorf("no image pin resolving to %s — the compose file has drifted from the running container; edit it by hand", ref)
}

// ErrNoContainers: the check compares what's running with the registries, so with no stack
// containers at all there is nothing to compare — which must not read as "everything is up to date".
var ErrNoContainers = errors.New("no stack containers exist yet, so there's nothing to compare — " +
	"set the stack up and start it first (dashboard setup wizard, or hsctl setup && hsctl up)")

// newerRelease checks the image's registry for a release tag newer than its pinned tag. "" when
// the tag isn't version-shaped, the registry is unreachable, or the pin is already the newest of
// its shape.
func (c *Checker) newerRelease(image string) string {
	host, repo, tag := registry.SplitRef(image)
	if _, _, ok := ParseTagVersion(tag); !ok {
		return ""
	}
	tags, err := c.Registry.Tags(registry.Base(host), repo)
	if err != nil {
		return ""
	}
	return NewerVersionTag(tag, tags)
}

// Check runs both probes for every stack container. progress, when non-nil, gets each status as
// it lands — the check talks to registries and takes seconds per image, so callers show rows live.
func (c *Checker) Check(progress func(Status)) ([]Status, error) {
	if err := c.Docker.Require(); err != nil {
		return nil, err
	}
	if _, err := c.Docker.Output(c.Repo.Root, "buildx", "version"); err != nil {
		return nil, fmt.Errorf("docker buildx is needed to read registry digests — install it:\n" +
			"  sudo apt-get install -y docker-buildx-plugin")
	}
	targets, err := c.Containers()
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, ErrNoContainers
	}
	var statuses []Status
	for _, name := range targets {
		var st Status
		if image := c.Docker.ContainerImage(name); image == "" {
			st = Status{Container: name, Image: "—", State: "container not created — skipped"}
		} else {
			localJoined, _ := c.Docker.Output(c.Repo.Root, "image", "inspect", "--format", "{{join .RepoDigests \",\"}}", image)
			remoteOut, _ := c.Docker.Output(c.Repo.Root, "buildx", "imagetools", "inspect", image)
			st = Compare(name, image, localJoined, remoteOut, c.newerRelease(image))
		}
		statuses = append(statuses, st)
		if progress != nil {
			progress(st)
		}
	}
	return statuses, nil
}

// Run checks every container and prints a report; with apply naming containers (or "all"), it
// then applies those updates.
func (c *Checker) Run(apply []string, yes bool) error {
	fmt.Fprintf(c.Out, "checking the stack's containers against their registries...\n\n")
	var stale int
	statuses, err := c.Check(func(st Status) {
		mark := "  "
		if st.Stale {
			mark = "! "
			stale++
		}
		fmt.Fprintf(c.Out, "%s%-16s %-42s %s\n", mark, st.Container, st.Image, st.State)
	})
	if err != nil {
		return err
	}
	if len(apply) > 0 {
		return c.Apply(statuses, apply, yes)
	}
	if stale == 0 {
		fmt.Fprintln(c.Out, "\neverything is up to date.")
		return nil
	}
	fmt.Fprintf(c.Out, "\n%d image(s) have an update. To apply (after trying it in the sandbox — see README):\n", stale)
	fmt.Fprintln(c.Out, "  hsctl updates --apply all            # routine updates only")
	fmt.Fprintln(c.Out, "  hsctl updates --apply <container>    # one app, majors included")
	return nil
}

// Apply performs the chosen updates. For a NEWER RELEASE it first records the new pin in the
// service's gitignored .env (the tracked docker-compose.yml, which declares
// `image: ${VAR:-default}`, is never touched); either way the service is then compose pull + up -d.
// "all" selects every stale image but skips major jumps — Postgres or Nextcloud majors need their
// own upgrade paths, so those must be named explicitly (and confirmed) one at a time.
func (c *Checker) Apply(statuses []Status, targets []string, yes bool) error {
	var chosen []Status
	if len(targets) == 1 && targets[0] == "all" {
		for _, st := range statuses {
			if !st.Stale {
				continue
			}
			if st.Major() {
				fmt.Fprintf(c.Out, "\nskipping %s: %s -> %s is a major upgrade — test it in the sandbox, then run:\n  hsctl updates --apply %s\n",
					st.Container, registry.Tag(st.Image), st.Newer, st.Container)
				continue
			}
			chosen = append(chosen, st)
		}
	} else {
		byName := map[string]Status{}
		for _, st := range statuses {
			byName[st.Container] = st
		}
		for _, t := range targets {
			st, ok := byName[t]
			if !ok {
				return fmt.Errorf("no stack container named %q (see the list above)", t)
			}
			if !st.Stale {
				fmt.Fprintf(c.Out, "nothing to apply for %s (%s)\n", t, st.State)
				continue
			}
			chosen = append(chosen, st)
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintln(c.Out, "\nnothing to apply.")
		return nil
	}

	fmt.Fprintln(c.Out, "\nwill apply:")
	for _, st := range chosen {
		if st.Newer == "" {
			fmt.Fprintf(c.Out, "  %-16s repull %s (its tag was rebuilt upstream)\n", st.Container, st.Image)
			continue
		}
		fmt.Fprintf(c.Out, "  %-16s %s -> %s (pins it in %s/.env)\n", st.Container, registry.Tag(st.Image), st.Newer, layout.FolderFor(st.Container))
		if st.Major() {
			fmt.Fprintf(c.Out, "  %-16s ^ MAJOR upgrade — make sure you've tested it in the sandbox and have a fresh backup\n", "")
		}
	}
	if !yes {
		if !c.Ask.CanAsk() {
			return fmt.Errorf("no terminal to confirm on — re-run with --yes")
		}
		if !c.Ask.YesNo("apply now?", false) {
			return fmt.Errorf("aborted — nothing was changed")
		}
	}

	for _, st := range chosen {
		if st.Newer == "" {
			continue
		}
		svc := layout.FolderFor(st.Container)
		dir := c.Repo.Path(svc)
		data, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
		if err != nil {
			return err
		}
		env, err := envfile.Read(filepath.Join(dir, ".env"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		varName, err := PinVarFor(string(data), env, st.Image)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(svc, "docker-compose.yml"), err)
		}
		newRef := registry.Retag(st.Image, st.Newer)
		if err := envfile.Upsert(filepath.Join(dir, ".env"), varName, newRef); err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "pinned %s=%s in %s\n", varName, newRef, filepath.Join(svc, ".env"))
	}

	done := map[string]bool{}
	for _, st := range chosen {
		svc := layout.FolderFor(st.Container)
		if done[svc] {
			continue
		}
		done[svc] = true
		fmt.Fprintf(c.Out, "\n== updating %s ==\n", svc)
		dir := c.Repo.Path(svc)
		if err := c.Docker.Run(dir, c.Docker.ComposePullArgs()...); err != nil {
			return fmt.Errorf("%s: pull: %w", svc, err)
		}
		if err := c.Docker.Run(dir, c.Docker.ComposeUpArgs()...); err != nil {
			return fmt.Errorf("%s: up: %w", svc, err)
		}
	}
	fmt.Fprintln(c.Out, "\ndone — open the apps to confirm they still work (a Check will now show them up to date).")
	return nil
}
