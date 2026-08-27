package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The true uninstall: `sudo hsctl uninstall` removes everything hsctl and install.sh
// put on the box — apps, images, systemd units, sandbox leftovers, generated
// config/secrets, and the hsctl binary itself — but KEEPS every byte of user data by
// default: the app docker volumes, and the backup config + local restic snapshots.
//   --data  also deletes the app volumes (all app data)
//   --all   deletes the app volumes AND the backups
// Deleting data always demands a typed double confirmation on a terminal; --yes only
// skips the prompt for the data-keeping default. The repo checkout is never deleted —
// it may hold the backups, and for a source install it's the user's own clone — the
// final message prints the one command left to remove it. CLI-only on purpose: it
// tears down the dashboard service that Command Center cards run under.

// appContainers is each compose file's fixed container_name — swept by name too, so a
// renamed or removed app dir can't leave its containers running after an uninstall.
var appContainers = []string{"caddy", "vaultwarden", "nextcloud-app", "nextcloud-db", "nextcloud-redis", "pihole", "stirling-pdf", "it-tools", "imagetools"}

// composeProjects are the volume-name prefixes compose has ever used for the stack
// ("<project>_<volume>", project = app dir name) — the canonical set is swept alongside
// the current dirs so leftovers from a since-renamed dir go too.
var composeProjects = []string{"caddy", "vaultwarden", "nextcloud", "pihole", "stirling", "it-tools", "imagetools"}

// backupFiles is what the default tier keeps and --all deletes, relative to the repo.
var backupFiles = []string{"backup.conf", ".restic-password", ".backup-env", "backups"}

// Sandbox artifact names — keep in step with the root Makefile's SANDBOX_* vars.
const (
	sandboxName  = "hsctl-sandbox"
	sandboxImage = "hsctl-sandbox"
	sandboxVol   = "hsctl-sandbox-data"
)

var uninstallUnits = []string{"hsctl-ui.service", "hsctl-backup.service", "hsctl-backup.timer"}

// confirmTyped asks the user to type word exactly, n times. False on any mismatch.
func confirmTyped(word string, n int) bool {
	for i := 1; i <= n; i++ {
		fmt.Printf("Confirm %d/%d — type %s to continue: ", i, n, word)
		line, _ := stdinReader.ReadString('\n')
		if strings.TrimSpace(line) != word {
			fmt.Println("No match — nothing was removed.")
			return false
		}
	}
	return true
}

func runQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// volumeInProjects reports whether a docker volume belongs to one of the compose
// projects (compose names volumes "<project>_<volume>").
func volumeInProjects(name string, projects []string) bool {
	for _, p := range projects {
		if strings.HasPrefix(name, p+"_") {
			return true
		}
	}
	return false
}

func cmdUninstall(data, all, yes bool) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("hsctl uninstall needs root — re-run as:\n  sudo hsctl uninstall")
	}
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	data = data || all

	appDirs, _ := filepath.Glob(filepath.Join(repo, "*", "docker-compose.yml"))
	sort.Strings(appDirs)

	fmt.Println("This removes the homeserver stack from this machine: every app's containers")
	fmt.Println("and images, the shared docker network, the dashboard service + backup timer,")
	fmt.Println("the test sandbox, the generated config/secrets in the repo, and hsctl itself.")
	switch {
	case all:
		fmt.Println("\n--all: the app data volumes AND the backups (backup.conf, .restic-password,")
		fmt.Println(".backup-env, backups/ — every local restic snapshot) are PERMANENTLY deleted.")
		fmt.Println("There is no undo.")
	case data:
		fmt.Println("\n--data: the app data volumes (ALL APP DATA) are PERMANENTLY deleted too.")
		fmt.Println("Kept: the backups (backup.conf, .restic-password, .backup-env, backups/).")
	default:
		fmt.Println("\nKept: the app data volumes (your data) and the backups — restorable any time.")
		fmt.Println("(--data also deletes the volumes; --all deletes volumes and backups.)")
	}
	fmt.Println()

	// Deleting data always demands a typed, interactive double confirmation — --yes
	// only covers the tier that keeps every byte of user data.
	if data {
		if !isTTY() {
			return fmt.Errorf("deleting data needs an interactive terminal to confirm on — no flag skips this")
		}
		if !confirmTyped("DESTROY", 2) {
			return fmt.Errorf("aborted")
		}
	} else if !yes {
		if !isTTY() {
			return fmt.Errorf("no terminal to confirm on — re-run with --yes")
		}
		if !confirmTyped("YES", 1) {
			return fmt.Errorf("aborted")
		}
	}

	fmt.Println("\n== stopping + removing systemd units ==")
	for _, u := range uninstallUnits {
		_ = runQuiet("systemctl", "disable", "--now", u)
		_ = os.Remove("/etc/systemd/system/" + u)
	}
	_ = runQuiet("systemctl", "daemon-reload")
	_ = runQuiet("systemctl", "reset-failed")

	if dockerCmd(repo, "info").Run() != nil {
		fmt.Println("\nwarning: Docker isn't reachable — skipping container/volume/image cleanup.")
	} else {
		fmt.Println("== removing app containers and images ==")
		downArgs := []string{"compose", "down", "--rmi", "all", "--remove-orphans"}
		if data {
			downArgs = append(downArgs, "-v")
		}
		for _, f := range appDirs {
			fmt.Printf("-- %s\n", filepath.Base(filepath.Dir(f)))
			if err := dockerRun(filepath.Dir(f), downArgs...); err != nil {
				fmt.Printf("   warning: %v (continuing)\n", err)
			}
		}
		fmt.Println("-- sweeping orphaned app containers (renamed/removed dirs)")
		for _, c := range appContainers {
			if runQuiet("docker", "rm", "-f", c) == nil {
				fmt.Printf("   removed container %s\n", c)
			}
		}
		if data {
			fmt.Println("-- sweeping orphaned volumes from old compose projects")
			projects := append([]string{}, composeProjects...)
			for _, f := range appDirs {
				projects = append(projects, filepath.Base(filepath.Dir(f)))
			}
			if out, err := dockerOut(repo, "volume", "ls", "--format", "{{.Name}}"); err == nil {
				for _, v := range strings.Fields(out) {
					if volumeInProjects(v, projects) && runQuiet("docker", "volume", "rm", v) == nil {
						fmt.Printf("   removed volume %s\n", v)
					}
				}
			}
		}
		_ = runQuiet("docker", "network", "rm", edgeNetwork)

		fmt.Println("== removing the test sandbox (container, image, cached data) ==")
		_ = runQuiet("docker", "stop", "-t", "8", sandboxName)
		// The sandbox's nested loop device outlives its container; the sweep needs the
		// sandbox image's own tooling, so it happens before the image is removed.
		_ = runQuiet("docker", "run", "--rm", "--privileged", "--entrypoint", "bash", sandboxImage,
			"-c", "losetup -a 2>/dev/null | grep -i sandboxdisk | cut -d: -f1 | xargs -r -n1 losetup -d")
		_ = runQuiet("docker", "rm", "-f", sandboxName)
		if runQuiet("docker", "volume", "rm", sandboxVol) == nil {
			fmt.Printf("   removed volume %s\n", sandboxVol)
		}
		if runQuiet("docker", "rmi", "-f", sandboxImage) == nil {
			fmt.Printf("   removed image %s\n", sandboxImage)
		}
		_ = runQuiet("docker", "image", "prune", "-f")
	}
	_ = os.RemoveAll(filepath.Join(repo, "sandbox", "_build"))

	fmt.Println("== removing generated config/secrets in the repo ==")
	generated, _ := filepath.Glob(filepath.Join(repo, "*", ".env"))
	for _, rel := range []string{".ui-password", "setup.conf", "WELCOME.txt", "caddy-root-ca.crt", "pihole/custom.list", "hsctl/hsctl"} {
		generated = append(generated, filepath.Join(repo, rel))
	}
	for _, f := range generated {
		_ = os.Remove(f)
	}
	_ = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".hsctl-tmp-") {
			_ = os.Remove(path)
		}
		return nil
	})

	if all {
		fmt.Println("== deleting backups ==")
		if kv, err := readKV(filepath.Join(repo, "backup.conf")); err == nil && kv["RESTIC_REPO"] != "" {
			fmt.Printf("   restic repo was: %s\n", kv["RESTIC_REPO"])
		}
		for _, rel := range backupFiles {
			_ = os.RemoveAll(filepath.Join(repo, rel))
		}
	}

	fmt.Println("== removing hsctl itself ==")
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe)
	}
	_ = os.Remove("/usr/local/bin/hsctl")

	fmt.Println()
	switch {
	case all:
		fmt.Println("Everything removed, including all backups and their config.")
		fmt.Println("If the restic repo lived off-box (sftp:/b2:/s3:), delete it at that location too.")
	case data:
		fmt.Println("Uninstalled, app data deleted. Kept: the backups in the repo — restorable after a reinstall.")
	default:
		fmt.Println("Uninstalled. Kept: your app data (docker volumes) and the backups — a reinstall picks them straight up.")
	}
	fmt.Println("Docker itself was not removed.")
	fmt.Printf("The repo checkout was kept%s; when you're sure, remove it with:  sudo rm -rf %s\n",
		map[bool]string{true: "", false: " (it may hold your backups)"}[all], repo)
	return nil
}
