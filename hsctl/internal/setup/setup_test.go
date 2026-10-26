package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// TestDeployed: what the stack runs with comes from caddy/.env, not from a setup.conf that may
// already hold the next values.
func TestDeployed(t *testing.T) {
	cases := []struct {
		name, caddyEnv string
		wantIP         string
		wantPort       int
	}{
		{"not set up", "", "10.0.0.9", 9000},
		{"running stack", "SERVER_IP=10.0.0.5\nHOME_UPSTREAM=host.docker.internal:8088\n", "10.0.0.5", 8088},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := paths.Repo{Root: t.TempDir()}
			if tc.caddyEnv != "" {
				if err := os.MkdirAll(repo.Path("caddy"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo.Root, "caddy/.env"), []byte(tc.caddyEnv), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ip, port := Deployed(repo, config.Settings{ServerIP: "10.0.0.9", UIPort: 9000})
			if ip != tc.wantIP || port != tc.wantPort {
				t.Errorf("Deployed = %s %d, want %s %d", ip, port, tc.wantIP, tc.wantPort)
			}
		})
	}
}
