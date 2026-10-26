package sysinfo

import (
	"testing"
)

func TestParseCPULine(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	s, err := ParseCPULine("cpu  100 0 50 800 40 5 5 0")
	if err != nil {
		t.Fatal(err)
	}
	if s.total != 1000 {
		t.Errorf("total = %d, want 1000", s.total)
	}
	if s.busy != 160 { // everything except idle (800) and iowait (40)
		t.Errorf("busy = %d, want 160", s.busy)
	}
	if _, err := ParseCPULine("cpu0 1 2 3 4 5"); err == nil {
		t.Error("per-core line should be rejected")
	}
	if _, err := ParseCPULine("intr 12345"); err == nil {
		t.Error("non-cpu line should be rejected")
	}
	if _, err := ParseCPULine("cpu 1 2 x 4 5"); err == nil {
		t.Error("garbage field should be rejected")
	}
}

func TestCPUUsagePercent(t *testing.T) {
	a := CPUSample{busy: 100, total: 1000}
	if got := CPUPercent(a, CPUSample{busy: 150, total: 1100}); got != 50 {
		t.Errorf("usage = %d, want 50", got)
	}
	// No elapsed time (or a counter reset) must not divide by zero.
	if got := CPUPercent(a, a); got != 0 {
		t.Errorf("same-sample usage = %d, want 0", got)
	}
	if got := CPUPercent(a, CPUSample{busy: 10, total: 100}); got != 0 {
		t.Errorf("reset-counter usage = %d, want 0", got)
	}
}

func TestParseMemInfo(t *testing.T) {
	total, avail, err := ParseMemInfo("MemTotal:       16326428 kB\nMemFree:         1000000 kB\nMemAvailable:   10000000 kB\nBuffers:          500000 kB\n")
	if err != nil {
		t.Fatal(err)
	}
	if total != 16326428 || avail != 10000000 {
		t.Errorf("got total=%d avail=%d", total, avail)
	}
	if _, _, err := ParseMemInfo("MemFree: 12345 kB\n"); err == nil {
		t.Error("missing MemTotal/MemAvailable should be an error")
	}
}

func TestHumanGiB(t *testing.T) {
	if got := HumanGiB(16 * 1024 * 1024); got != "16.0 GiB" {
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
		status, ok := SmartVerdict(c.out)
		if status != c.status || ok != c.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, status, ok, c.status, c.ok)
		}
	}
}

func TestStatDiskSpace(t *testing.T) {
	ds := DiskSpaceOf("/")
	if !ds.OK || ds.Pct < 0 || ds.Pct > 100 || ds.Used == "" || ds.Total == "" {
		t.Errorf("implausible root disk space: %+v", ds)
	}
	if ds := DiskSpaceOf("/nonexistent-path-xyz"); ds.OK {
		t.Errorf("missing path should not be OK: %+v", ds)
	}
}
