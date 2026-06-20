package main

import (
	"html/template"
	"strings"
	"testing"
)

// TestMigrateTemplateRenders parses + executes migrateTmpl for every authority state, so a
// template syntax error or a bad field/`eq` reference fails the build rather than 500-ing live.
func TestMigrateTemplateRenders(t *testing.T) {
	cases := []struct {
		name string
		data migrateData
		want string // a substring that must appear for this state
	}{
		{"home", migrateData{Authority: "home"}, "running at HOME"},
		{"cloud", migrateData{Authority: "cloud", Provider: "hetzner", CloudVMID: "vm1", CloudAddr: "cloudbox", HasVM: true}, "running in the CLOUD"},
		{"unknown", migrateData{Authority: "unknown", LastError: "corrupt marker"}, "state is unknown"},
	}
	tmpl, err := template.New("p").Funcs(tmplFuncs).Parse(migrateTmpl)
	if err != nil {
		t.Fatalf("parse migrateTmpl: %v", err)
	}
	for _, c := range cases {
		var b strings.Builder
		if err := tmpl.Execute(&b, c.data); err != nil {
			t.Fatalf("%s: execute: %v", c.name, err)
		}
		out := b.String()
		if !strings.Contains(out, "Cloud migration") {
			t.Errorf("%s: missing page title", c.name)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: missing %q for this state", c.name, c.want)
		}
	}
	// The destroy control appears only when a VM is recorded.
	var b strings.Builder
	_ = tmpl.Execute(&b, migrateData{Authority: "home"})
	if strings.Contains(b.String(), "Destroy cloud VM") {
		t.Error("destroy control should be hidden when no VM is recorded")
	}
}
