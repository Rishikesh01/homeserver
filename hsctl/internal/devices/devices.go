// Package devices lists the block devices attached to the machine and mounts or ejects them — the
// guided, one-click flow the dashboard's Drives page exposes.
package devices

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// lsblk does the discovery (rich, scriptable, present on every Debian/Ubuntu box) and the plain
// `mount`/`umount` commands the action. We deliberately DON'T touch
// /etc/fstab: this stack mounts its backup HDD by hand on purpose (see the
// backup-disk-manual-mount note), so a mount here is a one-shot that disappears on
// reboot — exactly the manual model the operator already relies on.
//
// Mounting needs root. The UI runs as root under systemd (see hsctl-ui.service); a local
// `hsctl ui` dev run that isn't root goes through sudo (package priv).

// MountRoot is where attached disks get mounted. One subdir per disk keeps several
// disks from colliding on a single /mnt.
const MountRoot = "/mnt"

// BlockDevice mirrors one node of `lsblk --json` output. Disks carry Children
// (their partitions); a partition is a leaf. Fields we don't request stay zero.
type BlockDevice struct {
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
	Children   []BlockDevice `json:"children"`
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

// DevPath returns the absolute device path, falling back to /dev/<name> when an older
// lsblk doesn't emit the PATH column.
func (d BlockDevice) DevPath() string {
	if d.Path != "" {
		return d.Path
	}
	return "/dev/" + d.Name
}

// List returns the tree of disks (each with its partitions) via lsblk.
func (s *Service) List() ([]BlockDevice, error) {
	cols := "NAME,KNAME,PATH,SIZE,TYPE,FSTYPE,LABEL,UUID,MOUNTPOINT,MODEL,RM,RO"
	out, err := exec.Command("lsblk", "--json", "-o", cols).Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk failed (is util-linux installed?): %w", err)
	}
	var root struct {
		Blockdevices []BlockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("parse lsblk json: %w", err)
	}
	return root.Blockdevices, nil
}

// Row is the flattened, display-ready view of one mountable node for the UI.
type Row struct {
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
	Ejectable  bool   // mounted where a data disk lives (/mnt, /media) — safe to offer Eject
	Why        string // when not Mountable, a short reason
	Suggest    string // default mount directory, pre-filled in the UI (editable by the operator)
}

// fsBlocklist are pseudo/again-not-data filesystems we never offer to mount: swap has
// no files, and the LUKS/LVM/RAID member types must be opened through their own
// mappers, not mounted directly.
var fsBlocklist = map[string]bool{
	"swap": true, "crypto_LUKS": true, "LVM2_member": true,
	"linux_raid_member": true, "zfs_member": true,
}

// Rows flattens the lsblk tree into the rows the UI shows. A node is mountable
// when it has a real, recognised filesystem and isn't already mounted. We surface
// non-mountable nodes too (greyed out, with a reason) so the operator can SEE the disk
// is there even when we won't act on it.
func Rows(devs []BlockDevice) []Row {
	var rows []Row
	add := func(d BlockDevice, parent string, isDisk bool) {
		r := Row{
			Path: d.DevPath(), Name: d.Name, Parent: parent, Size: d.Size,
			FSType: d.FSType, Label: d.Label, Mountpoint: d.Mountpoint,
			Removable: bool(d.RM), ReadOnly: bool(d.RO), IsDisk: isDisk,
		}
		switch {
		case d.Mountpoint != "":
			r.Mountable = false
			r.Why = "already mounted"
			r.Ejectable = Ejectable(d.Mountpoint)
			if !r.Ejectable {
				r.Why = "system disk — can't be ejected here"
			}
		case d.FSType == "":
			r.Mountable = false
			r.Why = "no filesystem"
		case fsBlocklist[d.FSType]:
			r.Mountable = false
			r.Why = d.FSType + " — open via its mapper, not mountable directly"
		default:
			r.Mountable = true
			r.Suggest = TargetFor(r) // a default the operator can accept or edit
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

// TargetFor picks the directory under /mnt for a device: its filesystem label when
// it has a clean one, else the kernel name (sdb1). Always a single path component.
func TargetFor(d Row) string {
	name := labelRe.ReplaceAllString(d.Label, "")
	if name == "" || name == "." || name == ".." {
		name = labelRe.ReplaceAllString(d.Name, "")
	}
	if name == "" {
		name = "disk"
	}
	return filepath.Join(MountRoot, name)
}

// find re-reads lsblk and returns the row for devPath, so every mount/unmount
// validates the request against the CURRENT device list. This is the security gate: the
// UI only ever passes a path back that we listed, but re-checking here means a forged
// POST can't make us mount("/dev/../etc", ...) or umount an arbitrary path.
func (s *Service) find(devPath string) (Row, error) {
	devs, err := s.List()
	if err != nil {
		return Row{}, err
	}
	for _, r := range Rows(devs) {
		if r.Path == devPath {
			return r, nil
		}
	}
	return Row{}, fmt.Errorf("device %q is not an attached block device", devPath)
}

// CleanTarget validates and normalises an operator-chosen mount directory: it must be an
// absolute path and not the filesystem root. (The operator picks the directory in the UI, the
// same as a manual `mount <disk> <dir>`; this just keeps it a sane place to mount.)
func CleanTarget(target string) (string, error) {
	t := filepath.Clean(strings.TrimSpace(target))
	if !filepath.IsAbs(t) || t == "/" {
		return "", fmt.Errorf("mount directory must be an absolute path (got %q)", target)
	}
	return t, nil
}

// Mount mounts the given block device and returns the mountpoint. It validates the
// device against a fresh lsblk, refuses devices we deem non-mountable (already mounted, no
// filesystem, LUKS/swap/…), creates the target dir, and runs `mount`. target=="" uses the
// per-label default under /mnt; a non-empty target is the directory the operator chose (they
// pick it in the UI, like a manual mount) — validated to be an absolute path. No /etc/fstab
// entry is written — this is a one-shot mount, gone on reboot.
func (s *Service) Mount(devPath, target string) (string, error) {
	d, err := s.find(devPath)
	if err != nil {
		return "", err
	}
	if !d.Mountable {
		return "", fmt.Errorf("can't mount %s: %s", d.Path, d.Why)
	}
	if target == "" {
		target = TargetFor(d)
	}
	target, err = CleanTarget(target)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return "", fmt.Errorf("create mountpoint %s: %w", target, err)
	}
	// If the directory already holds a mount (e.g. a different disk), bail rather than
	// stack a second filesystem over it.
	if isMountpoint(target) {
		return "", fmt.Errorf("%s is already a mountpoint — unmount it first", target)
	}
	if out, err := s.Priv.Command("mount", d.Path, target).CombinedOutput(); err != nil {
		return "", fmt.Errorf("mount %s %s: %v\n%s", d.Path, target, err, strings.TrimSpace(string(out)))
	}
	return target, nil
}

// Ejectable reports whether a mountpoint is where attached data disks live: /mnt itself or
// anything under /mnt, /media or /run/media. Only those get an Eject button: unmounting /,
// /boot/efi or /home from a web page breaks the running system (or, for the EFI partition,
// quietly breaks the next kernel/GRUB update).
func Ejectable(mp string) bool {
	if mp == "/mnt" {
		return true
	}
	for _, root := range []string{"/mnt/", "/media/", "/run/media/"} {
		if strings.HasPrefix(mp, root) && len(mp) > len(root) {
			return true
		}
	}
	return false
}

// Unmount unmounts the given block device (validated against lsblk's current
// mountpoint for it). Takes the device path so the UI's Eject button matches the Mount
// button; we resolve it to the live mountpoint and umount that.
func (s *Service) Unmount(devPath string) (string, error) {
	devs, err := s.List()
	if err != nil {
		return "", err
	}
	var mp string
	for _, r := range Rows(devs) {
		if r.Path == devPath {
			mp = r.Mountpoint
		}
	}
	if mp == "" {
		return "", fmt.Errorf("%s is not mounted", devPath)
	}
	if !Ejectable(mp) {
		return "", fmt.Errorf("%s is mounted at %s, a system location — only disks under /mnt or /media can be ejected here", devPath, mp)
	}
	if out, err := s.Priv.Command("umount", devPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("umount %s: %v\n%s", devPath, err, strings.TrimSpace(string(out)))
	}
	return mp, nil
}

// isMountpoint reports whether path is itself a mountpoint (via `mountpoint -q`).
func isMountpoint(path string) bool {
	return exec.Command("mountpoint", "-q", path).Run() == nil
}

// Privileged runs a command as root.
type Privileged interface {
	Command(name string, args ...string) *exec.Cmd
}

// Service lists, mounts and ejects block devices.
type Service struct {
	Priv Privileged
}

// New returns the devices service, running mount/umount through priv.
func New(priv Privileged) *Service { return &Service{Priv: priv} }
