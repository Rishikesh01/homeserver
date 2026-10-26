package itest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// AdminUID and AdminGID own the test checkouts, as the person who cloned the repo would.
const AdminUID, AdminGID = 1000, 1000

// stackFiles are the real stack's tracked files a checkout needs, relative to the repo.
var stackFiles = []string{
	"services.json", "ONBOARDING.md", "setup.conf.example", "backup.conf.example",
	"caddy/docker-compose.yml", "caddy/Caddyfile", "caddy/.env.example",
	"vaultwarden/docker-compose.yml", "vaultwarden/.env.example",
	"nextcloud/docker-compose.yml", "nextcloud/.env.example",
	"pihole/docker-compose.yml", "pihole/.env.example", "pihole/custom.list.example",
	"stirling/docker-compose.yml", "it-tools/docker-compose.yml",
	"imagetools/docker-compose.yml", "imagetools/html/index.html",
}

// Checkout copies the real stack — its compose files, Caddyfile, examples, services.json — into a
// temp folder, owned by the stand-in admin when the test runs as root: a fresh clone to set up and
// start. Everything it starts is torn down (volumes included) when the test ends.
func Checkout(t *testing.T) paths.Repo {
	t.Helper()
	src := SourceRoot(t)
	root := filepath.Join(t.TempDir(), "homeserver")
	for _, rel := range stackFiles {
		b, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if os.Geteuid() == 0 {
		Chown(t, filepath.Dir(root), AdminUID, AdminGID)
	}
	repo := paths.Repo{Root: root}
	t.Cleanup(func() { Teardown(repo) })
	return repo
}

// Teardown stops every compose project in repo and deletes its volumes and the shared network.
func Teardown(repo paths.Repo) {
	files, _ := filepath.Glob(repo.Path("*", "docker-compose.yml"))
	for _, f := range files {
		c := exec.Command("docker", "compose", "down", "-v", "--remove-orphans", "-t", "1")
		c.Dir = filepath.Dir(f)
		_ = c.Run()
	}
	_ = exec.Command("docker", "network", "rm", "homeserver-edge").Run()
}

// LightNextcloud swaps the Nextcloud app (a 1.4 GB image) for busybox through a compose override,
// keeping the real compose file's database, cache, volumes and container names — what backups and
// restores work on.
func LightNextcloud(t testing.TB, repo paths.Repo) {
	t.Helper()
	WriteFile(t, repo.Root, "nextcloud/docker-compose.override.yml", `services:
  app:
    image: `+Busybox+`
    command: ["sleep", "86400"]
`)
}

// LoopDisk attaches a fresh 16 MB ext4 image labelled label as a loop device for the rest of the
// test, and returns the device. Loop devices are the host kernel's: one created after the test
// container started has no node in its /dev yet, so a missing node is made first.
func LoopDisk(t *testing.T, label string) string {
	t.Helper()
	NeedRoot(t)
	img := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(img, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(img, 16<<20); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mkfs.ext4", "-q", "-L", label, img).CombinedOutput(); err != nil {
		t.Skipf("mkfs.ext4: %v %s", err, out)
	}
	var dev string
	for attempt := 0; attempt < 3 && dev == ""; attempt++ {
		out, err := exec.Command("losetup", "-f").Output()
		if err != nil {
			t.Skipf("no loop devices here: %v", err)
		}
		free := strings.TrimSpace(string(out))
		if _, err := os.Stat(free); os.IsNotExist(err) {
			var n int
			if _, err := fmt.Sscanf(free, "/dev/loop%d", &n); err != nil {
				t.Fatalf("losetup -f: %q", free)
			}
			if out, err := exec.Command("mknod", free, "b", "7", fmt.Sprint(n)).CombinedOutput(); err != nil {
				t.Fatalf("mknod %s: %v %s", free, err, out)
			}
		}
		// Another process can take the device between the lookup and the attach: try again.
		if exec.Command("losetup", free, img).Run() == nil {
			dev = free
		}
	}
	if dev == "" {
		t.Fatal("couldn't attach a loop device")
	}
	t.Cleanup(func() {
		_ = exec.Command("umount", dev).Run()
		_ = exec.Command("losetup", "-d", dev).Run()
	})
	// lsblk reads the label from udev's database; make sure it's there.
	_ = exec.Command("udevadm", "trigger", "--settle", dev).Run()
	return dev
}
