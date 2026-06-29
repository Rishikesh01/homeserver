package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFlexBool covers the lsblk boolean variants across util-linux versions, since one
// unparsed variant would break the whole device listing.
func TestFlexBool(t *testing.T) {
	cases := map[string]bool{`true`: true, `false`: false, `"1"`: true, `"0"`: false,
		`"true"`: true, `"false"`: false, `1`: true, `0`: false, `""`: false}
	for in, want := range cases {
		var b flexBool
		if err := json.Unmarshal([]byte(in), &b); err != nil {
			t.Fatalf("unmarshal %q: %v", in, err)
		}
		if bool(b) != want {
			t.Errorf("flexBool(%s) = %v, want %v", in, bool(b), want)
		}
	}
}

// TestMountTargetStaysUnderMnt is a security test: a hostile filesystem LABEL (or device
// name) must never let a mount target escape /mnt via "../" or absolute paths.
func TestMountTargetStaysUnderMnt(t *testing.T) {
	for _, label := range []string{"../etc", "../../root", "/etc/passwd", "..", ".", "a/b", "bad name;rm", ""} {
		got := mountTargetFor(deviceRow{Label: label, Name: "sdb1"})
		if !strings.HasPrefix(got, mountRoot+"/") {
			t.Errorf("label %q -> %q, escaped %s", label, got, mountRoot)
		}
		if strings.Contains(got, "..") {
			t.Errorf("label %q -> %q contains ..", label, got)
		}
	}
	// A clean label is used verbatim; a missing one falls back to the device name.
	if got := mountTargetFor(deviceRow{Label: "backup", Name: "sdb1"}); got != "/mnt/backup" {
		t.Errorf("clean label -> %q, want /mnt/backup", got)
	}
	if got := mountTargetFor(deviceRow{Label: "", Name: "sdb1"}); got != "/mnt/sdb1" {
		t.Errorf("empty label -> %q, want /mnt/sdb1", got)
	}
}

// TestMountableRows checks the classification: a partition with a real fs is mountable,
// an already-mounted one isn't, swap/no-fs aren't, and loop/rom disks are skipped.
func TestMountableRows(t *testing.T) {
	devs := []blockDevice{
		{Name: "sda", Type: "disk", Model: "SysDisk", Children: []blockDevice{
			{Name: "sda1", Path: "/dev/sda1", Type: "part", FSType: "ext4", Mountpoint: "/"}, // mounted -> no
			{Name: "sda2", Path: "/dev/sda2", Type: "part", FSType: "swap"},                  // swap -> no
		}},
		{Name: "sdb", Type: "disk", Model: "Backup", Children: []blockDevice{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part", FSType: "ext4", Label: "backup"}, // mountable
			{Name: "sdb2", Path: "/dev/sdb2", Type: "part", FSType: ""},                      // no fs -> no
		}},
		{Name: "sdc", Type: "disk", FSType: "vfat", Path: "/dev/sdc"}, // whole-disk fs -> mountable
		{Name: "loop0", Type: "loop"},                                 // skipped
		{Name: "sr0", Type: "rom"},                                    // skipped
	}
	rows := mountableRows(devs)
	byPath := map[string]deviceRow{}
	for _, r := range rows {
		byPath[r.Path] = r
	}
	wantMountable := map[string]bool{"/dev/sda1": false, "/dev/sda2": false,
		"/dev/sdb1": true, "/dev/sdb2": false, "/dev/sdc": true}
	for p, want := range wantMountable {
		r, ok := byPath[p]
		if !ok {
			t.Fatalf("row %s missing from output", p)
		}
		if r.Mountable != want {
			t.Errorf("%s mountable=%v (why=%q), want %v", p, r.Mountable, r.Why, want)
		}
	}
	if _, ok := byPath["/dev/loop0"]; ok {
		t.Error("loop device should be skipped")
	}
	if _, ok := byPath["/dev/sr0"]; ok {
		t.Error("rom device should be skipped")
	}
}
