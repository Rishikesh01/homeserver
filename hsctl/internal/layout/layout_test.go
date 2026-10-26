package layout

import (
	"testing"
)

func TestServiceDirFor(t *testing.T) {
	for in, want := range map[string]string{
		"nextcloud-db": "nextcloud", "nextcloud-app": "nextcloud", "nextcloud-redis": "nextcloud",
		"stirling-pdf": "stirling", "vaultwarden": "vaultwarden", "caddy": "caddy", "it-tools": "it-tools",
	} {
		if got := FolderFor(in); got != want {
			t.Errorf("FolderFor(%s) = %s, want %s", in, got, want)
		}
	}
}
