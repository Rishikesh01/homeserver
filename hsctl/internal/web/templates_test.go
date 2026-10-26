package web

import (
	"html/template"
	"io"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/devices"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
	"github.com/Rishikesh01/homeserver/hsctl/internal/sysinfo"
	"github.com/Rishikesh01/homeserver/hsctl/internal/updates"
)

// TestTemplatesParseAndExecute renders every UI template against representative data.
// Template bugs (a stray "}}" in embedded JS, a renamed struct field, a bad pipeline)
// otherwise only surface when a real request hits render() — this catches them at
// `go test` time instead. It uses the SAME funcs render() uses.
func TestTemplatesParseAndExecute(t *testing.T) {
	groups := []cmdGroup{}
	for _, cat := range webCmdCategories {
		g := cmdGroup{Name: cat}
		for _, c := range webCmds {
			if c.Category == cat {
				g.Cmds = append(g.Cmds, c)
			}
		}
		groups = append(groups, g)
	}
	cfg := config.Settings{ServerIP: "192.168.1.10", TZ: "Etc/UTC"}
	apps := []stack.App{{Dir: "vaultwarden", Name: "Passwords", Icon: "🔐", Desc: "d", Enabled: true, Running: true},
		{Dir: "it-tools", Name: "it-tools"}}

	cases := []struct {
		name string
		tmpl string
		data any
	}{
		{"home", homeTmpl, homeData{Cfg: cfg,
			Services: []serviceLink{{Name: "Vaultwarden", Icon: "🔐", Desc: "Passwords", URL: "https://x"}}}},
		{"admin", adminTmpl, adminData{Cfg: cfg,
			Containers: []stack.Container{{Name: "caddy", State: "running", Status: "Up 2h", Running: true}},
			Flash:      flash{Msg: "hi"}, DockerErr: ""}},
		{"login", loginTmpl, loginData{Err: "", Next: "/admin"}},
		{"help", helpTmpl, helpData{Body: "<p>hi</p>"}},
		{"cert-missing", certMissingTmpl, nil},
		{"commands", commandsTmpl, commandsData{Groups: groups}},
		{"apps", appsTmpl, appsData{Apps: apps, Busy: "Start everything — running for 2m"}},
		{"setup", setupTmpl, setupData{Cfg: cfg, Apps: apps, Step: 1, Err: "bad IP"}},
		{"setup-logins", setupTmpl, setupData{Cfg: cfg, Step: 2, Secrets: []provision.Secret{{Label: "Pi-hole admin", Value: "x"}}}},
		{"devices", devicesTmpl, devicesData{MountAt: "/mnt", Flash: flash{Msg: "ok", Err: true}, GuardPath: "/mnt", GuardOK: false, Rows: []devices.Row{
			{Path: "/dev/sdb1", Name: "sdb1", Parent: "Seagate", Size: "1.8T", FSType: "ext4",
				Label: "backup", Mountpoint: "", Removable: true, Mountable: true},
			{Path: "/dev/sda1", Name: "sda1", Size: "200M", FSType: "vfat", Mountpoint: "/boot",
				Mountable: false, Why: "already mounted"},
			{Path: "/dev/sdc", Name: "sdc", Size: "50G", FSType: "", Mountable: false, Why: "no filesystem"},
		}}},
		{"terminal", terminalTmpl, nil},
		{"updates-unchecked", updatesTmpl, updatesData{}},
		{"updates", updatesTmpl, updatesData{Checked: true, Age: "3 min ago", StaleN: 2, RoutineN: 1, Rows: []updatesRow{
			{Status: updates.Status{Container: "vaultwarden", Image: "vaultwarden/server:1.37.0",
				State: "NEWER RELEASE 1.37.1 (pinned to 1.37.0)", Stale: true, Newer: "1.37.1"}},
			{Status: updates.Status{Container: "nextcloud-db", Image: "postgres:16-alpine",
				State: "NEWER RELEASE 18-alpine (pinned to 16-alpine)", Stale: true, Newer: "18-alpine"}, Major: true},
			{Status: updates.Status{Container: "it-tools", Image: "ghcr.io/corentinth/it-tools:2024.10.22-7ca5933",
				State: "up to date"}},
			{Status: updates.Status{Container: "pihole", Image: "pihole/pihole:2025.04.0",
				State: "couldn't read the registry digest — offline, rate-limited, or a private image"}},
		}}},
		{"backup", backupTmpl, backupData{Repo: "/mnt/backup/restic", Retention: backup.DefaultRetention,
			ResticOK: true, ResticVersion: "0.16.4", GuardPath: "/mnt/backup", GuardOK: false, HasPassword: true,
			Stats: "Total Size: 1.2 GiB", Snapshots: "ID  Time\nabc 2026", Flash: flash{Msg: "done"}}},
		{"restore", restoreTmpl, restoreData{Snapshots: "ID  Time", ResticOK: true}},
		{"restore-progress", restoreProgressTmpl, struct{ JobID string }{"abc"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := template.New(tc.name).Funcs(tmplFuncs).Parse(tc.tmpl)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.name, err)
			}
			if err := tmpl.Execute(io.Discard, tc.data); err != nil {
				t.Fatalf("execute %s: %v", tc.name, err)
			}
		})
	}
}

// TestAdminTmplRenders: the admin page shows the system health and backup age it's given.
func TestAdminTmplRenders(t *testing.T) {
	d := adminData{
		Cfg:    config.Settings{ServerIP: "192.168.1.2", TZ: "UTC"},
		Backup: backup.Freshness{Known: true, Age: "9 days ago", Stale: true},
		Sys: sysinfo.Stats{
			CPUPct: 42, CPUOK: true, Cores: 8, Load1: "0.55",
			MemUsed: "5.2 GiB", MemTotal: "15.6 GiB", MemPct: 33, MemOK: true,
			RootDisk:   sysinfo.DiskSpace{Used: "40.0 GiB", Total: "119.2 GiB", Pct: 34, OK: true},
			BackupDisk: sysinfo.DiskSpace{Used: "200.0 GiB", Total: "931.5 GiB", Pct: 21, OK: true},
			Disks: []sysinfo.DiskHealth{
				{Path: "/dev/sda", Model: "Samsung SSD 870", Size: "465.8G", Status: "healthy", OK: true},
				{Path: "/dev/sdb", Model: "WDC WD40EFRX", Size: "3.6T", Status: "FAILING"},
				{Path: "/dev/sdc", Size: "14.9G", Status: "unknown"},
			},
		},
	}
	var sb strings.Builder
	tpl, err := template.New("admin").Funcs(tmplFuncs).Parse(adminTmpl)
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
