//go:build integration

package sysinfo_test

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/devices"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
	"github.com/Rishikesh01/homeserver/hsctl/internal/sysinfo"
)

// TestParsers read /proc's formats and smartctl's verdicts.
func TestParsers(t *testing.T) {
	a, errA := sysinfo.ParseCPULine("cpu  100 0 50 800 40 5 5 0") // user nice system idle iowait irq softirq steal
	b, errB := sysinfo.ParseCPULine("cpu  150 0 50 850 40 5 5 0")
	total, avail, errM := sysinfo.ParseMemInfo("MemTotal:       16326428 kB\nMemFree:         1000000 kB\nMemAvailable:   10000000 kB\n")
	_, _, errMissing := sysinfo.ParseMemInfo("MemFree: 12345 kB\n")
	bad := []string{"cpu0 1 2 3 4 5", "intr 12345", "cpu 1 2 x 4 5"}
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"cpu line parses", errA == nil && errB == nil, true},
		{"busy share between samples", sysinfo.CPUPercent(a, b), 50},
		{"no elapsed time", sysinfo.CPUPercent(a, a), 0},
		{"counter reset", sysinfo.CPUPercent(b, a), 0},
		{"meminfo", errM == nil && total == 16326428 && avail == 10000000, true},
		{"meminfo without the fields", errMissing != nil, true},
		{"GiB", sysinfo.HumanGiB(16 * 1024 * 1024), "16.0 GiB"},
	}
	for _, s := range bad {
		_, err := sysinfo.ParseCPULine(s)
		cases = append(cases, struct {
			name string
			got  any
			want any
		}{"rejects " + s, err != nil, true})
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	verdicts := []struct {
		out, status string
		ok          bool
	}{
		{"SMART overall-health self-assessment test result: PASSED\n", "healthy", true},
		{"SMART Health Status: OK\n", "healthy", true},
		{"SMART overall-health self-assessment test result: FAILED!\n", "FAILING", false},
		{"SMART Health Status: FAILING\n", "FAILING", false},
		{"/dev/sdb: Unknown USB bridge [0x1234:0x5678]\nPlease specify device type with the -d option.\n", "unknown", false},
		{"", "unknown", false},
	}
	for _, v := range verdicts {
		if status, ok := sysinfo.SmartVerdict(v.out); status != v.status || ok != v.ok {
			t.Errorf("SmartVerdict(%q) = %s %v", v.out, status, ok)
		}
	}
}

// TestGather reads this machine: CPU and memory from /proc, disk space, the disk list — and the
// backup disk's card when its guard is set, or the guard's warning when it isn't mounted.
func TestGather(t *testing.T) {
	mounted := t.TempDir()
	cases := []struct {
		name      string
		disk      func() (string, bool)
		wantDisk  bool
		wantGuard string
	}{
		{"no guard", func() (string, bool) { return "", false }, false, ""},
		{"backup disk mounted", func() (string, bool) { return mounted, true }, true, ""},
		{"backup disk missing", func() (string, bool) { return "/mnt/backup", false }, false, "/mnt/backup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := sysinfo.New(devices.New(priv.Root{}), priv.Root{}, tc.disk).Gather()
			if st.Cores != runtime.NumCPU() || !st.CPUOK || st.CPUPct < 0 || st.CPUPct > 100 || st.Load1 == "" {
				t.Errorf("CPU: %+v", st)
			}
			if !st.MemOK || st.MemPct <= 0 || st.MemPct >= 100 || !strings.HasSuffix(st.MemTotal, " GiB") {
				t.Errorf("memory: %+v", st)
			}
			if !st.RootDisk.OK || st.RootDisk.Total == "" {
				t.Errorf("root disk: %+v", st.RootDisk)
			}
			if st.BackupDisk.OK != tc.wantDisk || st.BackupGuard != tc.wantGuard {
				t.Errorf("backup disk %+v, guard %q", st.BackupDisk, st.BackupGuard)
			}
			if st.DisksErr != "" {
				t.Errorf("listing disks: %s", st.DisksErr)
			}
			if _, err := exec.LookPath("smartctl"); err != nil && !strings.Contains(st.SmartMsg, "apt-get install -y smartmontools") {
				t.Errorf("without smartctl there should be a hint: %q", st.SmartMsg)
			}
			for _, d := range st.Disks {
				if !strings.HasPrefix(d.Path, "/dev/") || strings.Contains(d.Path, "loop") {
					t.Errorf("not a physical disk: %+v", d)
				}
			}
		})
	}
	if ds := sysinfo.DiskSpaceOf("/nonexistent-path-xyz"); ds.OK {
		t.Errorf("a missing path should not be OK: %+v", ds)
	}
}
