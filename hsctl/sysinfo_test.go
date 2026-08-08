package main

import (
	"strings"
	"testing"
)

func TestParseCPULine(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	s, err := parseCPULine("cpu  100 0 50 800 40 5 5 0")
	if err != nil {
		t.Fatal(err)
	}
	if s.total != 1000 {
		t.Errorf("total = %d, want 1000", s.total)
	}
	if s.busy != 160 { // everything except idle (800) and iowait (40)
		t.Errorf("busy = %d, want 160", s.busy)
	}
	if _, err := parseCPULine("cpu0 1 2 3 4 5"); err == nil {
		t.Error("per-core line should be rejected")
	}
	if _, err := parseCPULine("intr 12345"); err == nil {
		t.Error("non-cpu line should be rejected")
	}
	if _, err := parseCPULine("cpu 1 2 x 4 5"); err == nil {
		t.Error("garbage field should be rejected")
	}
}

func TestCPUUsagePercent(t *testing.T) {
	a := cpuSample{busy: 100, total: 1000}
	if got := cpuUsagePercent(a, cpuSample{busy: 150, total: 1100}); got != 50 {
		t.Errorf("usage = %d, want 50", got)
	}
	// No elapsed time (or a counter reset) must not divide by zero.
	if got := cpuUsagePercent(a, a); got != 0 {
		t.Errorf("same-sample usage = %d, want 0", got)
	}
	if got := cpuUsagePercent(a, cpuSample{busy: 10, total: 100}); got != 0 {
		t.Errorf("reset-counter usage = %d, want 0", got)
	}
}

func TestParseMemInfo(t *testing.T) {
	total, avail, err := parseMemInfo("MemTotal:       16326428 kB\nMemFree:         1000000 kB\nMemAvailable:   10000000 kB\nBuffers:          500000 kB\n")
	if err != nil {
		t.Fatal(err)
	}
	if total != 16326428 || avail != 10000000 {
		t.Errorf("got total=%d avail=%d", total, avail)
	}
	if _, _, err := parseMemInfo("MemFree: 12345 kB\n"); err == nil {
		t.Error("missing MemTotal/MemAvailable should be an error")
	}
}

func TestHumanGiB(t *testing.T) {
	if got := humanGiB(16 * 1024 * 1024); got != "16.0 GiB" {
		t.Errorf("humanGiB = %q", got)
	}
}

func TestSmartVerdict(t *testing.T) {
	cases := []struct {
		name, out, status string
		ok                bool
	}{
		{"ata passed", "=== START OF READ SMART DATA SECTION ===\nSMART overall-health self-assessment test result: PASSED\n", "healthy", true},
		{"scsi ok", "SMART Health Status: OK\n", "healthy", true},
		{"nvme passed", "SMART overall-health self-assessment test result: PASSED\n", "healthy", true},
		{"ata failed", "SMART overall-health self-assessment test result: FAILED!\nDrive failure expected in less than 24 hours.\n", "FAILING", false},
		{"scsi failing", "SMART Health Status: FAILING\n", "FAILING", false},
		{"usb bridge", "/dev/sdb: Unknown USB bridge [0x1234:0x5678]\nPlease specify device type with the -d option.\n", "unknown", false},
		{"empty", "", "unknown", false},
	}
	for _, c := range cases {
		status, ok := smartVerdict(c.out)
		if status != c.status || ok != c.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, status, ok, c.status, c.ok)
		}
	}
}

// TestAdminTmplRenders makes sure the admin template (with the new System section)
// actually executes against a populated adminData — a template typo otherwise only
// blows up at request time.
func TestAdminTmplRenders(t *testing.T) {
	d := adminData{
		Cfg:    Config{ServerIP: "192.168.1.2", TZ: "UTC"},
		Backup: backupFreshness{Known: true, Age: "9 days ago", Stale: true},
		Sys: sysStats{
			CPUPct: 42, CPUOK: true, Cores: 8, Load1: "0.55",
			MemUsed: "5.2 GiB", MemTotal: "15.6 GiB", MemPct: 33, MemOK: true,
			RootDisk:   diskSpace{Used: "40.0 GiB", Total: "119.2 GiB", Pct: 34, OK: true},
			BackupDisk: diskSpace{Used: "200.0 GiB", Total: "931.5 GiB", Pct: 21, OK: true},
			Disks: []diskHealth{
				{Path: "/dev/sda", Model: "Samsung SSD 870", Size: "465.8G", Status: "healthy", OK: true},
				{Path: "/dev/sdb", Model: "WDC WD40EFRX", Size: "3.6T", Status: "FAILING"},
				{Path: "/dev/sdc", Size: "14.9G", Status: "unknown"},
			},
			SmartMsg: "",
		},
	}
	var sb strings.Builder
	tpl, err := parseTmpl(adminTmpl)
	if err != nil {
		t.Fatal(err)
	}
	if err := tpl.Execute(&sb, d); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{"42% busy", "33% used", "5.2 GiB", "Samsung SSD 870", "FAILING", "unknown",
		"40.0 GiB", "931.5 GiB", "9 days ago", "Overdue"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered admin page missing %q", want)
		}
	}
}
