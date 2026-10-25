package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestUpServicesCarriesOn: one app failing to start must not stop the ones after it — above all
// Caddy, the only way to reach the dashboard.
func TestUpServicesCarriesOn(t *testing.T) {
	cases := []struct {
		name       string
		failing    []string
		wantFailed []string
	}{
		{"all start", nil, nil},
		{"pihole can't bind :53", []string{"pihole"}, []string{"pihole"}},
		{"two apps fail", []string{"vaultwarden", "stirling"}, []string{"vaultwarden", "stirling"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var started []string
			failed := upServices(services, func(svc string) error {
				started = append(started, svc)
				if slices.Contains(tc.failing, svc) {
					return os.ErrInvalid
				}
				return nil
			})
			if !slices.Equal(started, services) {
				t.Errorf("started %v, want every service %v", started, services)
			}
			if !slices.Equal(failed, tc.wantFailed) {
				t.Errorf("failed = %v, want %v", failed, tc.wantFailed)
			}
		})
	}
}

// TestMissingEnvFor: `up` only needs the .env of apps that are switched on, plus Caddy's.
func TestMissingEnvFor(t *testing.T) {
	cases := []struct {
		name     string
		present  []string
		disabled []string
		want     []string
	}{
		{"nothing set up", nil, nil, []string{"vaultwarden", "nextcloud", "pihole", "caddy"}},
		{"all present", []string{"vaultwarden", "nextcloud", "pihole", "caddy"}, nil, nil},
		{"pihole off and never configured", []string{"vaultwarden", "nextcloud", "caddy"}, []string{"pihole"}, nil},
		{"caddy missing", []string{"vaultwarden", "nextcloud", "pihole"}, nil, []string{"caddy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := setupTestRepo(t)
			for _, d := range tc.present {
				if err := os.WriteFile(filepath.Join(repo, d, ".env"), []byte("X=1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := missingEnvFor(repo, Config{DisabledApps: tc.disabled})
			if !slices.Equal(got, tc.want) {
				t.Errorf("missing = %v, want %v", got, tc.want)
			}
		})
	}
}
