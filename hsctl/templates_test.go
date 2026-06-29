package main

import (
	"html/template"
	"io"
	"testing"
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

	cases := []struct {
		name string
		tmpl string
		data any
	}{
		{"home", homeTmpl, homeData{Cfg: Config{ServerIP: "192.168.1.10", TZ: "Etc/UTC"},
			Services: []serviceLink{{Name: "Vaultwarden", Icon: "🔐", Desc: "Passwords", URL: "https://x"}}}},
		{"admin", adminTmpl, adminData{Cfg: Config{ServerIP: "192.168.1.10", TZ: "Etc/UTC"},
			Containers: []containerStatus{{Name: "caddy", State: "running", Status: "Up 2h", Running: true}},
			Msg:        "hi", DockerErr: ""}},
		{"login", loginTmpl, loginData{Err: "", Next: "/admin"}},
		{"help", helpTmpl, helpData{Body: "<p>hi</p>"}},
		{"commands", commandsTmpl, commandsData{Groups: groups}},
		{"devices", devicesTmpl, devicesData{MountAt: "/mnt", Msg: "ok", Rows: []deviceRow{
			{Path: "/dev/sdb1", Name: "sdb1", Parent: "Seagate", Size: "1.8T", FSType: "ext4",
				Label: "backup", Mountpoint: "", Removable: true, Mountable: true},
			{Path: "/dev/sda1", Name: "sda1", Size: "200M", FSType: "vfat", Mountpoint: "/boot",
				Mountable: false, Why: "already mounted"},
			{Path: "/dev/sdc", Name: "sdc", Size: "50G", FSType: "", Mountable: false, Why: "no filesystem"},
		}}},
		{"terminal", terminalTmpl, nil},
		{"backup", backupTmpl, backupData{Repo: "/mnt/backup/restic", Retention: defaultRetention,
			ResticOK: true, ResticVersion: "0.16.4", GuardPath: "/mnt/backup", GuardOK: false,
			Stats: "Total Size: 1.2 GiB", Snapshots: "ID  Time\nabc 2026", Msg: "done"}},
		{"restore", restoreTmpl, restoreData{Snapshots: "ID  Time", Msg: "", ResticOK: true}},
	}

	for _, tc := range cases {
		tc := tc
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
