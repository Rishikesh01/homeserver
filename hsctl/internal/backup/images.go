package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/images"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
)

// ImageManifestFile records, inside staging, the image every compose pin ran when the backup was
// taken. Every backup writes it — it's a few hundred bytes — because data and the app version that
// wrote it belong together: Nextcloud refuses to start on data from a newer version than its image,
// and Postgres can't open another major version's data at all. Without the record a restore could
// only start the data on whatever this checkout pins now.
const ImageManifestFile = "images.json"

// ImageArchiveDir is where, inside staging, a backup that includes the images themselves
// (BACKUP_IMAGES=yes, or `backup run --images`) saves them with `docker save`, so a restore doesn't
// depend on a registry still serving those exact tags. They're deleted again once the snapshot holds
// them: the stack's images are a few GB, and restic deduplicates an unchanged image to almost
// nothing on the next run.
const ImageArchiveDir = "images"

// ImageRecord is one compose pin as it stood when the backup was taken.
type ImageRecord struct {
	Folder  string `json:"folder"`            // the compose project folder, e.g. "nextcloud"
	Var     string `json:"var,omitempty"`     // the pin's variable, e.g. NEXTCLOUD_IMAGE ("" for a bare pin)
	Ref     string `json:"ref"`               // the image compose ran, e.g. nextcloud:35.0.2-apache
	ID      string `json:"id,omitempty"`      // its local image ID; "" when it wasn't on the machine
	Digest  string `json:"digest,omitempty"`  // its registry digest (repo@sha256:…); "" if it was never pulled
	Archive string `json:"archive,omitempty"` // its saved copy in ImageArchiveDir; "" when not included
}

// ImageManifest is ImageManifestFile.
type ImageManifest struct {
	// Platform is the Docker daemon's os/arch (linux/amd64): what the saved images run on. A restore
	// onto another kind of machine pulls the same versions for its own platform instead.
	Platform string        `json:"platform,omitempty"`
	Images   []ImageRecord `json:"images"`
}

// stageImages records the image every compose pin resolves to — the compose default, or the .env
// override `hsctl updates --apply` wrote — in staging/images.json and, with save, saves each one
// that's on this machine into staging/images/. It returns what went wrong; the snapshot is still
// worth taking without the images. Whatever happens, the last run's saved images are cleared first
// and a record that can't be written is removed: a snapshot must never carry another run's images or
// versions, which a restore would put back.
func (s *Service) stageImages(staging string, save bool) (problems []string) {
	dir := filepath.Join(staging, ImageArchiveDir)
	manifest := filepath.Join(staging, ImageManifestFile)
	if err := os.RemoveAll(dir); err != nil {
		_ = os.Remove(manifest)
		return []string{"app images: clearing the last run's saved images failed: " + err.Error()}
	}
	pins, err := (&images.Checker{Repo: s.Repo}).Pins()
	if err != nil {
		_ = os.Remove(manifest)
		return []string{"app images: reading the image pins failed, so this snapshot doesn't record their versions: " + err.Error()}
	}
	if save {
		if err := fsutil.MkdirAllOwned(dir, 0700); err != nil {
			problems = append(problems, "app images: not saved: "+err.Error())
			save = false
		} else {
			fmt.Fprintln(s.Out, "app images: saving the images themselves into the snapshot...")
		}
	}
	m := ImageManifest{Platform: s.dockerPlatform()}
	saved := map[string]string{} // ref -> archive: an image two pins share is saved once
	taken := map[string]bool{}
	for _, p := range pins {
		r := ImageRecord{Folder: filepath.Dir(p.File), Var: p.Var, Ref: p.Image}
		r.ID, r.Digest = s.localImage(p.Image)
		if save {
			switch a, done := saved[r.Ref]; {
			case done:
				r.Archive = a
			case r.ID == "":
				fmt.Fprintf(s.Out, "  not on this machine, so not saved: %s (is %s switched off?)\n", r.Ref, r.Folder)
			default:
				name, size, err := s.saveImage(dir, r.Ref, taken)
				if err != nil {
					fmt.Fprintf(s.Err, "  couldn't save %s: %v\n", r.Ref, err)
					problems = append(problems, fmt.Sprintf("app images: %s wasn't saved: %s", r.Ref, FirstLine(err.Error())))
					break
				}
				fmt.Fprintf(s.Out, "  saved %s (%s)\n", r.Ref, humanSize(size))
				saved[r.Ref], r.Archive = name, name
			}
		}
		m.Images = append(m.Images, r)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		err = fsutil.WritePrivate(manifest, string(b)+"\n")
	}
	if err != nil {
		_ = os.Remove(manifest)
		return append(problems, "app images: recording their versions failed: "+err.Error())
	}
	fmt.Fprintf(s.Out, "app images: recorded the version of all %d pinned images\n", len(m.Images))
	return problems
}

// saveImage writes `docker save ref` into dir under a name of its own, and returns that name and
// the archive's size. A failed save leaves nothing behind for the snapshot to pick up.
func (s *Service) saveImage(dir, ref string, taken map[string]bool) (name string, size int64, err error) {
	name = archiveName(ref, taken)
	dst := filepath.Join(dir, name)
	f, err := fsutil.CreateOwned(dst, 0600)
	if err != nil {
		return "", 0, err
	}
	c := s.Docker.Command(s.Repo.Root, "save", ref)
	var errOut strings.Builder
	c.Stdout, c.Stderr = f, &errOut
	runErr := c.Run()
	closeErr := f.Close()
	if runErr == nil && closeErr == nil {
		if fi, err := os.Stat(dst); err == nil {
			return name, fi.Size(), nil
		}
	}
	_ = os.Remove(dst)
	if runErr == nil {
		runErr = closeErr
	}
	if msg := strings.TrimSpace(errOut.String()); msg != "" {
		return "", 0, fmt.Errorf("%v: %s", runErr, msg)
	}
	return "", 0, runErr
}

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// archiveName turns an image reference into a file name not already taken
// ("ghcr.io/stirling-tools/stirling-pdf:3.2.2" -> "ghcr.io_stirling-tools_stirling-pdf_3.2.2.tar").
func archiveName(ref string, taken map[string]bool) string {
	base := strings.Trim(unsafeNameChars.ReplaceAllString(ref, "_"), "._-")
	if base == "" {
		base = "image"
	}
	name := base + ".tar"
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d.tar", base, i)
	}
	taken[name] = true
	return name
}

// localImage returns ref's local image ID and registry digest, or "" when the image isn't on this
// machine. Asked quietly: an app that's switched off has never pulled its image, and that's fine.
func (s *Service) localImage(ref string) (id, digest string) {
	out, err := s.Docker.Command(s.Repo.Root, "image", "inspect", "--format", "{{.Id}} {{join .RepoDigests \" \"}}", ref).Output()
	if err != nil {
		return "", ""
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", ""
	}
	// An image tagged under several names has a digest for each: keep the one for this reference.
	host, repo, _ := registry.SplitRef(ref)
	for _, d := range f[1:] {
		if h, r, _ := registry.SplitRef(d); h == host && r == repo {
			return f[0], d
		}
	}
	if len(f) > 1 {
		return f[0], f[1]
	}
	return f[0], ""
}

// dockerPlatform is the Docker daemon's os/arch ("linux/amd64"), or "" if it can't say.
func (s *Service) dockerPlatform() string {
	out, err := s.Docker.Command(s.Repo.Root, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// humanSize formats a byte count for a person ("98 MB").
func humanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

var (
	envKeyRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	imageRefRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]*$`)
)

// ReadImageManifest reads a snapshot's images.json. A restore writes what it says into .env files
// and loads the files it names, so every entry has to be what a backup writes: a plain folder name,
// a variable name, an image reference, a file name inside the archive folder. Anything else and the
// whole record is refused — the restore then starts the data on this server's own pins.
func ReadImageManifest(path string) (ImageManifest, error) {
	var m ImageManifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", ImageManifestFile, err)
	}
	plain := func(name string) bool {
		return name != "" && name == filepath.Base(name) && !strings.HasPrefix(name, ".")
	}
	for _, r := range m.Images {
		switch {
		case !plain(r.Folder):
			return m, fmt.Errorf("%s: %q isn't a service folder", ImageManifestFile, r.Folder)
		case r.Var != "" && !envKeyRE.MatchString(r.Var):
			return m, fmt.Errorf("%s: %q isn't a variable name", ImageManifestFile, r.Var)
		case !imageRefRE.MatchString(r.Ref):
			return m, fmt.Errorf("%s: %q isn't an image reference", ImageManifestFile, r.Ref)
		case r.Archive != "" && !plain(r.Archive):
			return m, fmt.Errorf("%s: %q isn't a saved image's file name", ImageManifestFile, r.Archive)
		}
	}
	return m, nil
}

// pinKey identifies one compose pin: its folder and variable.
func pinKey(folder, varName string) string { return folder + "\x00" + varName }

// pinsNow is what each compose pin resolves to on this server right now, by pinKey.
func (s *Service) pinsNow() (map[string]string, error) {
	pins, err := (&images.Checker{Repo: s.Repo}).Pins()
	if err != nil {
		return nil, err
	}
	now := map[string]string{}
	for _, p := range pins {
		now[pinKey(filepath.Dir(p.File), p.Var)] = p.Image
	}
	return now, nil
}

// backupImages reads the image versions an extracted snapshot was taken with and says how they
// compare with what this server pins now. nil when there's nothing to go by — a backup from before
// hsctl recorded them, or an unreadable record — and the stack then starts on this server's pins.
func (s *Service) backupImages(p Plan) *ImageManifest {
	if p.Images == "" {
		fmt.Fprintln(s.Out, "this backup doesn't record its app versions (an older hsctl took it) — the stack starts on the images this server pins now")
		return nil
	}
	m, err := ReadImageManifest(p.Images)
	if err != nil {
		fmt.Fprintf(s.Err, "can't use the backup's record of its app versions (%v) — the stack starts on the images this server pins now\n", err)
		return nil
	}
	now, _ := s.pinsNow()
	fmt.Fprintln(s.Out, "the backup was taken on these app versions; the stack starts on them again:")
	for _, r := range m.Images {
		note := ""
		if r.Archive != "" {
			note = " · saved in the backup"
		}
		if cur, ok := now[pinKey(r.Folder, r.Var)]; ok && cur != r.Ref {
			note += " · this server pins " + cur + " now"
		}
		fmt.Fprintf(s.Out, "  %-12s %s%s\n", r.Folder, r.Ref, note)
	}
	return &m
}

// loadImages loads the images a snapshot saved (`docker load`) — before the stack is stopped, since
// it takes a while and doesn't touch what's running. An image already here as the very same build is
// skipped. One that can't be loaded isn't fatal: compose pulls that same version from its registry.
// Saved images for another kind of machine (a backup from a PC restored onto a Raspberry Pi) aren't
// loaded at all, for the same reason.
func (s *Service) loadImages(p Plan, m ImageManifest) {
	dir := filepath.Join(filepath.Dir(p.Images), ImageArchiveDir)
	var saved []ImageRecord
	seen := map[string]bool{}
	for _, r := range m.Images {
		if r.Archive != "" && !seen[r.Archive] {
			seen[r.Archive] = true
			saved = append(saved, r)
		}
	}
	if len(saved) == 0 {
		return // the backup didn't include the images: compose pulls the recorded versions
	}
	if here := s.dockerPlatform(); m.Platform != "" && here != "" && here != m.Platform {
		fmt.Fprintf(s.Out, "the images saved in the backup are for %s and this machine is %s — the same versions are pulled for it instead\n",
			m.Platform, here)
		return
	}
	fmt.Fprintln(s.Out, "loading the images saved in the backup...")
	for _, r := range saved {
		if id, _ := s.localImage(r.Ref); id != "" && id == r.ID {
			fmt.Fprintf(s.Out, "  %s — already on this machine\n", r.Ref)
			continue
		}
		f := filepath.Join(dir, r.Archive)
		if !fsutil.Exists(f) {
			fmt.Fprintf(s.Err, "  %s — its saved copy isn't in the snapshot; it's pulled from its registry instead\n", r.Ref)
			continue
		}
		if out, err := s.Docker.Combined(s.Repo.Root, "load", "-i", f); err != nil {
			fmt.Fprintf(s.Err, "  %s — couldn't load it (%v: %s); it's pulled from its registry instead\n", r.Ref, err, FirstLine(strings.TrimSpace(out)))
			continue
		}
		fmt.Fprintf(s.Out, "  %s — loaded\n", r.Ref)
	}
}

// pinImages points every compose pin back at the image the backup was taken with, through the same
// .env override `hsctl updates --apply` writes — no tracked file changes, and `hsctl updates` shows
// what's newer afterwards. It returns what it couldn't pin.
func (s *Service) pinImages(m ImageManifest) (problems []string) {
	now, err := s.pinsNow()
	if err != nil {
		return []string{"couldn't read this server's image pins, so the backup's app versions weren't pinned: " + err.Error()}
	}
	changed := 0
	for _, r := range m.Images {
		cur, ok := now[pinKey(r.Folder, r.Var)]
		switch {
		case !ok:
			fmt.Fprintf(s.Err, "  %s: %s no longer pins %s — it runs what it pins now\n",
				r.Folder, filepath.Join(r.Folder, "docker-compose.yml"), r.Ref)
			continue
		case cur == r.Ref:
			continue
		case r.Var == "":
			problems = append(problems, fmt.Sprintf("%s: a bare `image: %s` pin can't be moved back to %s — edit %s by hand",
				r.Folder, cur, r.Ref, filepath.Join(r.Folder, "docker-compose.yml")))
			continue
		}
		env := s.Repo.Path(r.Folder, ".env")
		if !fsutil.Exists(env) && slices.Contains(paths.EnvFiles, r.Folder+"/.env") {
			// A core app's .env holds its secrets: creating one with just the pin would make setup
			// believe the app is configured and never generate them.
			problems = append(problems, fmt.Sprintf("%s has no .env yet (run hsctl setup), so %s=%s wasn't pinned", r.Folder, r.Var, r.Ref))
			continue
		}
		if err := envfile.Upsert(env, r.Var, r.Ref); err != nil {
			problems = append(problems, fmt.Sprintf("pinning %s=%s in %s/.env failed: %v", r.Var, r.Ref, r.Folder, err))
			continue
		}
		fmt.Fprintf(s.Out, "  pinned %s=%s in %s/.env (was %s)\n", r.Var, r.Ref, r.Folder, cur)
		now[pinKey(r.Folder, r.Var)] = r.Ref
		changed++
	}
	switch {
	case changed > 0:
		fmt.Fprintln(s.Out, "  once the restore checks out, `hsctl updates` shows what's newer")
	case len(problems) == 0:
		fmt.Fprintln(s.Out, "  every app already pins the version the backup was taken with")
	}
	return problems
}
