//go:build integration

package devices_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/devices"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
)

// TestFlexBool reads lsblk's removable/read-only flags however a util-linux version writes them —
// one unparsed variant would break the whole listing.
func TestFlexBool(t *testing.T) {
	cases := map[string]bool{`true`: true, `false`: false, `"1"`: true, `"0"`: false,
		`"true"`: true, `"false"`: false, `1`: true, `0`: false, `""`: false, `null`: false}
	for in, want := range cases {
		var d devices.BlockDevice
		if err := json.Unmarshal([]byte(`{"name":"sdb","rm":`+in+`}`), &d); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if got := devices.Rows([]devices.BlockDevice{{Name: "sdb", Type: "disk", FSType: "ext4", RM: d.RM}})[0].Removable; got != want {
			t.Errorf("rm=%s -> removable %v, want %v", in, got, want)
		}
	}
}

// TestRows: what the Drives page offers — a real filesystem not yet mounted can be mounted, a
// mounted data disk ejected; system disks, swap, encrypted members and empty disks are shown but
// never acted on; snap images and empty drives aren't listed at all.
func TestRows(t *testing.T) {
	devs := []devices.BlockDevice{
		{Name: "sda", Type: "disk", Model: "SysDisk", Children: []devices.BlockDevice{
			{Name: "sda1", Path: "/dev/sda1", Type: "part", FSType: "ext4", Mountpoint: "/"},
			{Name: "sda2", Path: "/dev/sda2", Type: "part", FSType: "swap"},
			{Name: "sda3", Path: "/dev/sda3", Type: "part", FSType: "crypto_LUKS"},
			{Name: "sda4", Path: "/dev/sda4", Type: "part", FSType: "vfat", Mountpoint: "/boot/efi"},
		}},
		{Name: "sdb", Type: "disk", Model: "Backup", Children: []devices.BlockDevice{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part", FSType: "ext4", Label: "backup"},
			{Name: "sdb2", Path: "/dev/sdb2", Type: "part"},
			{Name: "sdb3", Path: "/dev/sdb3", Type: "part", FSType: "ext4", Mountpoint: "/mnt/old"},
		}},
		{Name: "sdc", Type: "disk", FSType: "vfat", Path: "/dev/sdc", Label: "USB STICK"},
		{Name: "loop0", Type: "loop"},
		{Name: "loop1", Type: "loop", Path: "/dev/loop1", FSType: "squashfs", Mountpoint: "/snap/x"},
		{Name: "loop2", Type: "loop", Path: "/dev/loop2", FSType: "ext4", Label: "sandboxdisk"},
		{Name: "loop3", Type: "loop", Path: "/dev/loop3", FSType: "ext4", Mountpoint: "/media/img"},
		{Name: "sr0", Type: "rom"},
	}
	cases := []struct {
		path      string
		mountable bool
		ejectable bool
		why       string
		suggest   string
		parent    string
	}{
		{"/dev/sda1", false, false, "system disk — can't be ejected here", "", "SysDisk"},
		{"/dev/sda2", false, false, "swap — open via its mapper, not mountable directly", "", "SysDisk"},
		{"/dev/sda3", false, false, "crypto_LUKS — open via its mapper, not mountable directly", "", "SysDisk"},
		{"/dev/sda4", false, false, "system disk — can't be ejected here", "", "SysDisk"},
		{"/dev/sdb1", true, false, "", "/mnt/backup", "Backup"},
		{"/dev/sdb2", false, false, "no filesystem", "", "Backup"},
		{"/dev/sdb3", false, true, "already mounted", "", "Backup"},
		{"/dev/sdc", true, false, "", "/mnt/USBSTICK", "sdc"},
		{"/dev/loop2", true, false, "", "/mnt/sandboxdisk", "loop device"},
		{"/dev/loop3", false, true, "already mounted", "", "loop device"},
	}
	rows := devices.Rows(devs)
	if len(rows) != len(cases) {
		t.Errorf("%d rows, want %d: %+v", len(rows), len(cases), rows)
	}
	byPath := map[string]devices.Row{}
	for _, r := range rows {
		byPath[r.Path] = r
	}
	for _, tc := range cases {
		r, ok := byPath[tc.path]
		if !ok {
			t.Errorf("%s not listed", tc.path)
			continue
		}
		if r.Mountable != tc.mountable || r.Ejectable != tc.ejectable || r.Why != tc.why || r.Suggest != tc.suggest || r.Parent != tc.parent {
			t.Errorf("%s = %+v", tc.path, r)
		}
	}
}

// TestTargets: a mount target always stays a single folder under /mnt whatever the label says, and
// only data-disk locations can be ejected.
func TestTargets(t *testing.T) {
	targets := []struct {
		label, name, want string
	}{
		{"backup", "sdb1", "/mnt/backup"},
		{"", "sdb1", "/mnt/sdb1"},
		{"../etc", "sdb1", "/mnt/etc"},
		{"../../root", "sdb1", "/mnt/root"},
		{"/etc/passwd", "sdb1", "/mnt/etcpasswd"},
		{"..", "sdb1", "/mnt/sdb1"},
		{"bad name;rm", "sdb1", "/mnt/badnamerm"},
		{"", "../x", "/mnt/x"},
		{"", "", "/mnt/disk"},
	}
	for _, tc := range targets {
		if got := devices.TargetFor(devices.Row{Label: tc.label, Name: tc.name}); got != tc.want {
			t.Errorf("TargetFor(%q, %q) = %s, want %s", tc.label, tc.name, got, tc.want)
		}
	}
	clean := []struct {
		in, want string
		ok       bool
	}{
		{"/mnt/backup", "/mnt/backup", true},
		{" /srv/disk/ ", "/srv/disk", true},
		{"/mnt/../srv/x", "/srv/x", true},
		{"/", "", false},
		{"relative/dir", "", false},
		{"", "", false},
	}
	for _, tc := range clean {
		got, err := devices.CleanTarget(tc.in)
		if got != tc.want || (err == nil) != tc.ok {
			t.Errorf("CleanTarget(%q) = %q, %v", tc.in, got, err)
		}
	}
	eject := map[string]bool{"/mnt": true, "/mnt/backup": true, "/media/usb": true, "/run/media/me/usb": true,
		"/": false, "/boot/efi": false, "/home": false, "/mntx": false, "/media/": false, "/srv/disk": false}
	for mp, want := range eject {
		if got := devices.Ejectable(mp); got != want {
			t.Errorf("Ejectable(%s) = %v, want %v", mp, got, want)
		}
	}
}

// TestMountAndEject mounts and ejects a real disk image the way the Drives page does, re-checking
// every request against lsblk.
func TestMountAndEject(t *testing.T) {
	itest.NeedRoot(t)
	label := strings.TrimPrefix(itest.Name("disk"), "hsctl-it-")
	dev := itest.LoopDisk(t, label)
	s := devices.New(priv.Root{})
	custom := filepath.Join(t.TempDir(), "srv-disk")
	if err := os.Mkdir(custom, 0755); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name    string
		run     func() (string, error)
		want    string
		wantErr string
	}{
		{"listed as mountable", func() (string, error) {
			devs, err := s.List()
			if err != nil {
				return "", err
			}
			for _, r := range devices.Rows(devs) {
				if r.Path == dev {
					if !r.Mountable {
						return r.Why, nil
					}
					return r.Suggest, nil
				}
			}
			return "not listed", nil
		}, "/mnt/" + label, ""},
		{"mount at the suggested folder", func() (string, error) { return s.Mount(dev, "") }, "/mnt/" + label, ""},
		{"mounted twice", func() (string, error) { return s.Mount(dev, "") }, "", "already mounted"},
		{"eject", func() (string, error) { return s.Unmount(dev) }, "/mnt/" + label, ""},
		{"eject again", func() (string, error) { return s.Unmount(dev) }, "", "is not mounted"},
		{"onto a folder that's already a mount", func() (string, error) {
			if err := exec.Command("mount", "-t", "tmpfs", "tmpfs", custom).Run(); err != nil {
				return "", err
			}
			defer exec.Command("umount", custom).Run()
			return s.Mount(dev, custom)
		}, "", "already a mountpoint"},
		{"a chosen folder", func() (string, error) { return s.Mount(dev, custom+"/") }, custom, ""},
		{"not ejected from a system location", func() (string, error) { return s.Unmount(dev) }, "", "a system location"},
		{"a forged device", func() (string, error) { return s.Mount("/dev/../etc", "") }, "", "is not an attached block device"},
		{"a relative folder", func() (string, error) {
			_ = exec.Command("umount", dev).Run()
			return s.Mount(dev, "mnt/x")
		}, "", "must be an absolute path"},
	}
	for _, st := range steps {
		got, err := st.run()
		if (err == nil) != (st.wantErr == "") || (err != nil && !strings.Contains(err.Error(), st.wantErr)) {
			t.Fatalf("%s: err = %v, want %q", st.name, err, st.wantErr)
		}
		if st.wantErr == "" && got != st.want {
			t.Fatalf("%s = %q, want %q", st.name, got, st.want)
		}
	}
}
