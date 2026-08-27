package main

import "testing"

func TestVolumeInProjects(t *testing.T) {
	projects := []string{"nextcloud", "caddy", "it-tools"}
	cases := map[string]bool{
		"nextcloud_db-data-v18": true,
		"caddy_caddy-data":      true,
		"it-tools_cache":        true,
		"nextcloudX_data":       false, // prefix must end at the underscore
		"hsctl-sandbox-data":    false, // sandbox volume is handled separately
		"unrelated_volume":      false,
	}
	for name, want := range cases {
		if got := volumeInProjects(name, projects); got != want {
			t.Errorf("volumeInProjects(%q) = %v, want %v", name, got, want)
		}
	}
}
