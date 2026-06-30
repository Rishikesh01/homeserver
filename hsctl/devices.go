package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Device management: list the block devices attached to the machine and mount the
// chosen one under /mnt — the guided, one-click flow the admin UI exposes. We use
// lsblk for discovery (rich, scriptable, present on every Debian/Ubuntu box) and the
// plain `mount`/`umount` commands for the action. We deliberately DON'T touch
// /etc/fstab: this stack mounts its backup HDD by hand on purpose (see the
// backup-disk-manual-mount note), so a mount here is a one-shot that disappears on
// reboot — exactly the manual model the operator already relies on.
//
// Mounting needs root. The UI runs as root under systemd (see hsctl-ui.service); a
// local `hsctl ui` dev run that isn't root falls back to sudo via privCmd.

// mountRoot is where attached disks get mounted. One subdir per disk keeps several
// disks from colliding on a single /mnt.
const mountRoot = "/mnt"

// blockDevice mirrors one node of `lsblk --json` output. Disks carry Children
// (their partitions); a partition is a leaf. Fields we don't request stay zero.
type blockDevice struct {
	Name       string        `json:"name"`
	KName      string        `json:"kname"`
	Path       string        `json:"path"`
	Size       string        `json:"size"`
	Type       string        `json:"type"` // "disk", "part", "loop", "rom", ...
	FSType     string        `json:"fstype"`
	Label      string        `json:"label"`
	UUID       string        `json:"uuid"`
	Mountpoint string        `json:"mountpoint"`
	Model      string        `json:"model"`
	RM         flexBool      `json:"rm"` // removable (USB stick / external)
	RO         flexBool      `json:"ro"` // read-only
	Children   []blockDevice `json:"children"`
}

// flexBool unmarshals lsblk's boolean-ish fields, which different util-linux versions
// emit as JSON true/false, the strings "0"/"1"/"true"/"false", or numbers. Without
// this, a single lsblk release variation would break the whole listing.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	switch strings.ToLower(s) {
	case "1", "true":
		*b = true
	default:
		*b = false
	}
	return nil
}

// devPath returns the absolute device path, falling back to /dev/<name> when an older
// lsblk doesn't emit the PATH column.
func (d blockDevice) devPath() string {
	if d.Path != "" {
		return d.Path
	}
	return "/dev/" + d.Name
}

// listBlockDevices returns the tree of disks (each with its partitions) via lsblk.
func listBlockDevices() ([]blockDevice, error) {
	cols := "NAME,KNAME,PATH,SIZE,TYPE,FSTYPE,LABEL,UUID,MOUNTPOINT,MODEL,RM,RO"
	out, err := exec.Command("lsblk", "--json", "-o", cols).Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk failed (is util-linux installed?): %w", err)
	}
	var root struct {
		Blockdevices []blockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("parse lsblk json: %w", err)
	}
	return root.Blockdevices, nil
}

// deviceRow is the flattened, display-ready view of one mountable node for the UI.
type deviceRow struct {
	Path       string // /dev/sdb1
	Name       string // sdb1
	Parent     string // disk model / name this partition lives on (context)
	Size       string
	FSType     string
	Label      string
	Mountpoint string // "" when not mounted
	Removable  bool
	ReadOnly   bool
	IsDisk     bool   // a whole disk with no partitions (some USB sticks)
	Mountable  bool   // we'd let the user mount it
	Why        string // when not Mountable, a short reason
}

// fsBlocklist are pseudo/again-not-data filesystems we never offer to mount: swap has
// no files, and the LUKS/LVM/RAID member types must be opened through their own
// mappers, not mounted directly.
var fsBlocklist = map[string]bool{
	"swap": true, "crypto_LUKS": true, "LVM2_member": true,
	"linux_raid_member": true, "zfs_member": true,
}

// mountableRows flattens the lsblk tree into the rows the UI shows. A node is mountable
// when it has a real, recognised filesystem and isn't already mounted. We surface
// non-mountable nodes too (greyed out, with a reason) so the operator can SEE the disk
// is there even when we won't act on it.
func mountableRows(devs []blockDevice) []deviceRow {
	var rows []deviceRow
	add := func(d blockDevice, parent string, isDisk bool) {
		r := deviceRow{
			Path: d.devPath(), Name: d.Name, Parent: parent, Size: d.Size,
			FSType: d.FSType, Label: d.Label, Mountpoint: d.Mountpoint,
			Removable: bool(d.RM), ReadOnly: bool(d.RO), IsDisk: isDisk,
		}
		switch {
		case d.Mountpoint != "":
			r.Mountable = false
			r.Why = "already mounted"
		case d.FSType == "":
			r.Mountable = false
			r.Why = "no filesystem"
		case fsBlocklist[d.FSType]:
			r.Mountable = false
			r.Why = d.FSType + " — open via its mapper, not mountable directly"
		default:
			r.Mountable = true
		}
		rows = append(rows, r)
	}
	for _, disk := range devs {
		if disk.Type == "rom" {
			continue // empty optical drive
		}
		if disk.Type == "loop" {
			// Most loop devices are system/snap images (read-only squashfs, or with no
			// filesystem) — hide those so the list stays clean. But surface a loop that
			// carries a real filesystem: a disk image an admin attached on purpose (and
			// what the test sandbox uses to demo mounting). Keep it visible even when
			// mounted, so it still gets an Eject button — same as a real partition does.
			if disk.FSType == "" || disk.FSType == "squashfs" {
				continue
			}
			add(disk, "loop device", true)
			continue
		}
		model := strings.TrimSpace(disk.Model)
		if model == "" {
			model = disk.Name
		}
		if len(disk.Children) == 0 {
			add(disk, model, true) // whole-disk filesystem (e.g. a freshly formatted USB stick)
			continue
		}
		for _, part := range disk.Children {
			add(part, model, false)
		}
	}
	return rows
}

// labelRe keeps mount-target names to a safe, predictable set — so a crafted volume
// label can't escape /mnt via "../" or shell-hostile characters.
var labelRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// mountTargetFor picks the directory under /mnt for a device: its filesystem label when
// it has a clean one, else the kernel name (sdb1). Always a single path component.
func mountTargetFor(d deviceRow) string {
	name := labelRe.ReplaceAllString(d.Label, "")
	if name == "" || name == "." || name == ".." {
		name = labelRe.ReplaceAllString(d.Name, "")
	}
	if name == "" {
		name = "disk"
	}
	return filepath.Join(mountRoot, name)
}

// findDeviceRow re-reads lsblk and returns the row for devPath, so every mount/unmount
// validates the request against the CURRENT device list. This is the security gate: the
// UI only ever passes a path back that we listed, but re-checking here means a forged
// POST can't make us mount("/dev/../etc", ...) or umount an arbitrary path.
func findDeviceRow(devPath string) (deviceRow, error) {
	devs, err := listBlockDevices()
	if err != nil {
		return deviceRow{}, err
	}
	for _, r := range mountableRows(devs) {
		if r.Path == devPath {
			return r, nil
		}
	}
	return deviceRow{}, fmt.Errorf("device %q is not an attached block device", devPath)
}

// mountDevice mounts the given block device at the per-label default under /mnt.
func mountDevice(devPath string) (string, error) { return mountDeviceAt(devPath, "") }

// mountDeviceAt mounts the given block device and returns the mountpoint. It validates the
// device against a fresh lsblk, refuses devices we deem non-mountable (already mounted, no
// filesystem, LUKS/swap/…), creates the target dir, and runs `mount`. target=="" uses the
// per-label default under /mnt; a non-empty target (the configured backup-guard path, so a
// UI mount can satisfy REQUIRE_MOUNT) is used as-is — the caller is responsible for vetting
// it. No /etc/fstab entry is written — this is a one-shot mount, gone on reboot.
func mountDeviceAt(devPath, target string) (string, error) {
	d, err := findDeviceRow(devPath)
	if err != nil {
		return "", err
	}
	if !d.Mountable {
		return "", fmt.Errorf("can't mount %s: %s", d.Path, d.Why)
	}
	if target == "" {
		target = mountTargetFor(d)
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return "", fmt.Errorf("create mountpoint %s: %w", target, err)
	}
	// If the directory already holds a mount (e.g. a different disk), bail rather than
	// stack a second filesystem over it.
	if mountpointOf(target) {
		return "", fmt.Errorf("%s is already a mountpoint — unmount it first", target)
	}
	if out, err := privCmd("mount", d.Path, target).CombinedOutput(); err != nil {
		return "", fmt.Errorf("mount %s %s: %v\n%s", d.Path, target, err, strings.TrimSpace(string(out)))
	}
	return target, nil
}

// unmountDevice unmounts the given block device (validated against lsblk's current
// mountpoint for it). Takes the device path so the UI's Eject button matches the Mount
// button; we resolve it to the live mountpoint and umount that.
func unmountDevice(devPath string) (string, error) {
	devs, err := listBlockDevices()
	if err != nil {
		return "", err
	}
	var mp string
	for _, r := range mountableRows(devs) {
		if r.Path == devPath {
			mp = r.Mountpoint
		}
	}
	if mp == "" {
		return "", fmt.Errorf("%s is not mounted", devPath)
	}
	if out, err := privCmd("umount", devPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("umount %s: %v\n%s", devPath, err, strings.TrimSpace(string(out)))
	}
	return mp, nil
}

// mountpointOf reports whether path is itself a mountpoint (via `mountpoint -q`).
func mountpointOf(path string) bool {
	return exec.Command("mountpoint", "-q", path).Run() == nil
}

// privCmd runs name as root: directly when we're already root (the systemd UI service),
// else via sudo for a local dev run.
func privCmd(name string, args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command(name, args...)
	}
	return exec.Command("sudo", append([]string{name}, args...)...)
}
