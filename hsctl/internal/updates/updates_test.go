package updates

import (
	"reflect"
	"testing"
)

const buildxOut = `Name:      docker.io/library/caddy:2
MediaType: application/vnd.oci.image.index.v1+json
Digest:    sha256:aaa111
`

func TestParseRemoteDigest(t *testing.T) {
	if got := ParseRemoteDigest(buildxOut); got != "sha256:aaa111" {
		t.Errorf("got %q", got)
	}
	if got := ParseRemoteDigest("ERROR: image not found"); got != "" {
		t.Errorf("garbage should yield empty, got %q", got)
	}
}

func TestParseLocalDigests(t *testing.T) {
	got := ParseLocalDigests("caddy@sha256:aaa111,docker.io/library/caddy@sha256:bbb222")
	if len(got) != 2 || got[0] != "sha256:aaa111" || got[1] != "sha256:bbb222" {
		t.Errorf("got %v", got)
	}
	if got := ParseLocalDigests(""); len(got) != 0 {
		t.Errorf("empty input should yield none, got %v", got)
	}
}

func TestCheckImage(t *testing.T) {
	up := Compare("caddy", "caddy:2", "caddy@sha256:aaa111", buildxOut, "")
	if up.Stale || up.State != "up to date" {
		t.Errorf("matching digests: %+v", up)
	}
	stale := Compare("caddy", "caddy:2", "caddy@sha256:old999", buildxOut, "")
	if !stale.Stale || stale.State != "UPDATE available" {
		t.Errorf("differing digests: %+v", stale)
	}
	if st := Compare("x", "x", "", buildxOut, ""); st.Stale || st.State == "up to date" {
		t.Errorf("no local digest must not claim a verdict: %+v", st)
	}
	if st := Compare("x", "x", "x@sha256:aaa", "ERROR", ""); st.Stale || st.State == "up to date" {
		t.Errorf("no remote digest must not claim a verdict: %+v", st)
	}
	// A newer release outranks a clean digest verdict: the pinned tag matching
	// the registry says nothing about newer pins existing.
	rel := Compare("vaultwarden", "vaultwarden/server:1.36.0", "v@sha256:aaa111", buildxOut, "1.37.0")
	if !rel.Stale || rel.State != "NEWER RELEASE 1.37.0 (pinned to 1.36.0)" {
		t.Errorf("newer release: %+v", rel)
	}
}

func TestParseTagVersion(t *testing.T) {
	for _, tc := range []struct {
		tag    string
		nums   []int
		suffix string
		ok     bool
	}{
		{"1.37.0", []int{1, 37, 0}, "", true},
		{"1.27-alpine", []int{1, 27}, "-alpine", true},
		{"30-apache", []int{30}, "-apache", true},
		{"2024.10.22-7ca5933", []int{2024, 10, 22}, "", true}, // build hash stripped
		{"2025.04.0", []int{2025, 4, 0}, "", true},
		{"latest", nil, "", false},
		{"apache", nil, "", false},
	} {
		nums, suffix, ok := ParseTagVersion(tc.tag)
		if ok != tc.ok || suffix != tc.suffix || !reflect.DeepEqual(nums, tc.nums) {
			t.Errorf("%s: got (%v, %q, %v)", tc.tag, nums, suffix, ok)
		}
	}
}

func TestNewerVersionTag(t *testing.T) {
	vwTags := []string{"latest", "1.36.0", "1.36.1", "1.37.0", "1.34.3-alpine", "testing"}
	if got := NewerVersionTag("1.36.0", vwTags); got != "1.37.0" {
		t.Errorf("pinned 1.36.0: got %q, want 1.37.0", got)
	}
	if got := NewerVersionTag("1.37.0", vwTags); got != "" {
		t.Errorf("newest pin must report nothing, got %q", got)
	}
	// Shape matters: same suffix, same number-count only.
	nginxTags := []string{"1.29-alpine", "1.29.1-alpine", "1.29", "mainline-alpine"}
	if got := NewerVersionTag("1.27-alpine", nginxTags); got != "1.29-alpine" {
		t.Errorf("suffix/shape filter: got %q, want 1.29-alpine", got)
	}
	// Build hashes compare by their version part.
	itTags := []string{"2024.10.22-7ca5933", "2025.5.1-abc1234"}
	if got := NewerVersionTag("2024.10.22-7ca5933", itTags); got != "2025.5.1-abc1234" {
		t.Errorf("build-hash pins: got %q, want 2025.5.1-abc1234", got)
	}
	if got := NewerVersionTag("latest", vwTags); got != "" {
		t.Errorf("non-version pin must report nothing, got %q", got)
	}
}

func TestIsMajorJump(t *testing.T) {
	for _, tc := range []struct {
		cur, next string
		want      bool
	}{
		{"16-alpine", "18-alpine", true}, // postgres generation
		{"30-apache", "34-apache", true}, // nextcloud majors are stepwise
		{"2.8-alpine", "2.11-alpine", false},
		{"1.37.0", "1.37.1", false},
		{"2025.04.0", "2026.07.2", false}, // calendar versions roll routinely
	} {
		if got := IsMajorJump(tc.cur, tc.next); got != tc.want {
			t.Errorf("IsMajorJump(%s, %s) = %v, want %v", tc.cur, tc.next, got, tc.want)
		}
	}
}

func TestPinVarFor(t *testing.T) {
	compose := "services:\n  vaultwarden:\n    image: ${VAULTWARDEN_IMAGE:-vaultwarden/server:1.36.0}\n    restart: unless-stopped\n"
	// No override yet: the running ref matches the compose default.
	v, err := PinVarFor(compose, nil, "vaultwarden/server:1.36.0")
	if err != nil || v != "VAULTWARDEN_IMAGE" {
		t.Fatalf("PinVarFor(default) = (%q, %v), want VAULTWARDEN_IMAGE", v, err)
	}
	// A previously applied update: the container runs the .env override, and the next
	// update must match through it — while the shadowed default matches nothing.
	env := map[string]string{"VAULTWARDEN_IMAGE": "vaultwarden/server:1.36.5"}
	v, err = PinVarFor(compose, env, "vaultwarden/server:1.36.5")
	if err != nil || v != "VAULTWARDEN_IMAGE" {
		t.Fatalf("PinVarFor(override) = (%q, %v), want VAULTWARDEN_IMAGE", v, err)
	}
	if _, err := PinVarFor(compose, env, "vaultwarden/server:1.36.0"); err == nil {
		t.Error("shadowed default should not match")
	}
	// Only the matching service resolves in a multi-image file.
	multi := "  db:\n    image: ${DB_IMAGE:-postgres:16-alpine}\n  app:\n    image: ${APP_IMAGE:-nextcloud:30-apache}\n"
	if v, err := PinVarFor(multi, nil, "postgres:16-alpine"); err != nil || v != "DB_IMAGE" {
		t.Errorf("PinVarFor(multi) = (%q, %v), want DB_IMAGE", v, err)
	}
	// A bare pin has no variable to override — refuse, don't guess.
	if _, err := PinVarFor("  image: postgres:16-alpine\n", nil, "postgres:16-alpine"); err == nil {
		t.Error("bare pin should error")
	}
	// A drifted compose file (nothing resolves to the running ref) must refuse.
	if _, err := PinVarFor(compose, nil, "vaultwarden/server:1.35.0"); err == nil {
		t.Error("drifted file should error")
	}
}

// TestRegistryTags runs the full client flow against a fake v2 registry:

// anonymous 401 -> bearer token fetch -> two Link-paginated pages.
