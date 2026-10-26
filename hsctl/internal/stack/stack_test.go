package stack

import (
	"errors"
	"slices"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
)

// TestUpEach: one app failing to start must not stop the ones after it — above all Caddy, the only
// way to reach the dashboard.
func TestUpEach(t *testing.T) {
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
			failed := UpEach(layout.Services, func(svc string) error {
				started = append(started, svc)
				if slices.Contains(tc.failing, svc) {
					return errors.New("boom")
				}
				return nil
			})
			if !slices.Equal(started, layout.Services) || !slices.Equal(failed, tc.wantFailed) {
				t.Errorf("started %v failed %v", started, failed)
			}
		})
	}
}

// TestStopOrder stops Caddy first — or not at all when the dashboard must stay reachable.
func TestStopOrder(t *testing.T) {
	cases := []struct {
		keepProxy bool
		first     string
		hasProxy  bool
	}{{false, "caddy", true}, {true, "imagetools", false}}
	for _, tc := range cases {
		got := StopOrder(tc.keepProxy)
		if got[0] != tc.first || slices.Contains(got, "caddy") != tc.hasProxy {
			t.Errorf("StopOrder(%v) = %v", tc.keepProxy, got)
		}
	}
}
