package main

import "testing"

const buildxOut = `Name:      docker.io/library/caddy:2
MediaType: application/vnd.oci.image.index.v1+json
Digest:    sha256:aaa111
`

func TestParseRemoteDigest(t *testing.T) {
	if got := parseRemoteDigest(buildxOut); got != "sha256:aaa111" {
		t.Errorf("got %q", got)
	}
	if got := parseRemoteDigest("ERROR: image not found"); got != "" {
		t.Errorf("garbage should yield empty, got %q", got)
	}
}

func TestParseLocalDigests(t *testing.T) {
	got := parseLocalDigests("caddy@sha256:aaa111,docker.io/library/caddy@sha256:bbb222")
	if len(got) != 2 || got[0] != "sha256:aaa111" || got[1] != "sha256:bbb222" {
		t.Errorf("got %v", got)
	}
	if got := parseLocalDigests(""); len(got) != 0 {
		t.Errorf("empty input should yield none, got %v", got)
	}
}

func TestCheckImage(t *testing.T) {
	up := checkImage("caddy", "caddy:2", "caddy@sha256:aaa111", buildxOut)
	if up.Stale || up.State != "up to date" {
		t.Errorf("matching digests: %+v", up)
	}
	stale := checkImage("caddy", "caddy:2", "caddy@sha256:old999", buildxOut)
	if !stale.Stale || stale.State != "UPDATE available" {
		t.Errorf("differing digests: %+v", stale)
	}
	if st := checkImage("x", "x", "", buildxOut); st.Stale || st.State == "up to date" {
		t.Errorf("no local digest must not claim a verdict: %+v", st)
	}
	if st := checkImage("x", "x", "x@sha256:aaa", "ERROR"); st.Stale || st.State == "up to date" {
		t.Errorf("no remote digest must not claim a verdict: %+v", st)
	}
}
