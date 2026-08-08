package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// System health for the admin dashboard: live CPU + memory usage from /proc, and a
// per-disk SMART health verdict via smartctl. Everything degrades gracefully — a
// missing /proc field or an uninstalled smartmontools hides that stat (with a hint)
// instead of breaking the page.

// sysStats is the display-ready system section of the admin page. Zero-value fields
// mean "couldn't read it" and the template hides them.
type sysStats struct {
	CPUPct   int    // busy % across all cores over the sample window
	CPUOK    bool   // CPUPct is real (0% is a valid reading, so a flag, not a sentinel)
	Cores    int
	Load1    string // 1-minute load average, "" if unreadable
	MemUsed  string // human GiB
	MemTotal string
	MemPct   int
	MemOK    bool
	Disks    []diskHealth
	DisksErr string // listing disks failed entirely
	SmartMsg string // hint when smartctl isn't installed
}

// cpuSample is one reading of the aggregate "cpu" line of /proc/stat.
type cpuSample struct{ busy, total uint64 }

// parseCPULine parses the aggregate line ("cpu  user nice system idle iowait ...").
// Idle time is idle+iowait; everything else counts as busy.
func parseCPULine(line string) (cpuSample, error) {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuSample{}, fmt.Errorf("not an aggregate cpu line: %q", line)
	}
	var s cpuSample
	for i, v := range f[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return cpuSample{}, fmt.Errorf("parse /proc/stat field %d: %w", i+1, err)
		}
		s.total += n
		if i != 3 && i != 4 { // fields 4/5 are idle and iowait
			s.busy += n
		}
	}
	return s, nil
}

// cpuUsagePercent returns the busy percentage between two samples (0..100).
func cpuUsagePercent(a, b cpuSample) int {
	if b.total <= a.total {
		return 0
	}
	return int((b.busy - a.busy) * 100 / (b.total - a.total))
}

func sampleCPU() (cpuSample, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return parseCPULine(line)
}

// parseMemInfo pulls MemTotal and MemAvailable (kB) out of /proc/meminfo content.
// MemAvailable is the kernel's own estimate of what's actually free for new work —
// the right number for "used" (total-available), unlike MemFree which ignores caches.
func parseMemInfo(s string) (totalKB, availKB uint64, err error) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			totalKB, _ = strconv.ParseUint(f[1], 10, 64)
		case "MemAvailable:":
			availKB, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	if totalKB == 0 || availKB == 0 {
		return 0, 0, fmt.Errorf("MemTotal/MemAvailable not found in meminfo")
	}
	return totalKB, availKB, nil
}

// humanGiB renders a kB count as GiB with one decimal ("15.6 GiB").
func humanGiB(kb uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(kb)/(1024*1024))
}

// diskHealth is one physical disk's SMART verdict for the UI.
type diskHealth struct {
	Path   string // /dev/sda
	Model  string
	Size   string
	Status string // "healthy", "FAILING", "unknown"
	OK     bool
}

// smartVerdict extracts the overall health verdict from `smartctl -H` output. The
// wording differs by device type — ATA says "self-assessment test result: PASSED",
// SCSI says "Health Status: OK", NVMe uses the ATA phrasing — so match the verdict
// words, not the label. Unrecognised output (USB bridges smartctl can't talk
// through, etc.) comes back "unknown" rather than guessing.
func smartVerdict(out string) (status string, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		l := strings.ToLower(line)
		if !strings.Contains(l, "health") && !strings.Contains(l, "self-assessment") {
			continue
		}
		switch {
		case strings.Contains(l, "passed") || strings.Contains(l, ": ok"):
			return "healthy", true
		case strings.Contains(l, "failed") || strings.Contains(l, "failing"):
			return "FAILING", false
		}
	}
	return "unknown", false
}

// diskHealthList runs a SMART health check on every physical disk (lsblk type
// "disk"; loops/roms/zram never have SMART). Returns the rows plus a hint when
// smartctl isn't installed, or an error string when listing disks failed outright.
func diskHealthList() (rows []diskHealth, smartMsg, listErr string) {
	devs, err := listBlockDevices()
	if err != nil {
		return nil, "", err.Error()
	}
	_, lookErr := exec.LookPath("smartctl")
	haveSmart := lookErr == nil
	if !haveSmart {
		smartMsg = "smartctl isn't installed, so disk health can't be checked. Install it: sudo apt-get install -y smartmontools"
	}
	for _, d := range devs {
		if d.Type != "disk" || strings.HasPrefix(d.Name, "zram") {
			continue
		}
		row := diskHealth{Path: d.devPath(), Model: strings.TrimSpace(d.Model), Size: d.Size, Status: "unknown"}
		if haveSmart {
			// smartctl exits non-zero for several non-fatal reasons (bit-flag exit
			// codes), so parse whatever output came back and ignore the error.
			out, _ := privCmd("smartctl", "-H", row.Path).CombinedOutput()
			row.Status, row.OK = smartVerdict(string(out))
		}
		rows = append(rows, row)
	}
	return rows, smartMsg, ""
}

// gatherSysStats collects the whole system section. It blocks for the CPU sample
// window (~300ms), so callers run it concurrently with their other work.
func gatherSysStats() sysStats {
	var st sysStats
	st.Cores = runtime.NumCPU()
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			st.Load1 = f[0]
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		if total, avail, err := parseMemInfo(string(b)); err == nil {
			st.MemUsed, st.MemTotal = humanGiB(total-avail), humanGiB(total)
			st.MemPct = int((total - avail) * 100 / total)
			st.MemOK = true
		}
	}
	// The disk checks run during the CPU sample window, so SMART probes don't add
	// their own wall-clock on top of it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		st.Disks, st.SmartMsg, st.DisksErr = diskHealthList()
	}()
	if a, err := sampleCPU(); err == nil {
		time.Sleep(300 * time.Millisecond)
		if b, err := sampleCPU(); err == nil {
			st.CPUPct, st.CPUOK = cpuUsagePercent(a, b), true
		}
	}
	<-done
	return st
}
