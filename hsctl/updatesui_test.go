package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The apply endpoint's whole safety story is its validation: only "all" or a
// container name from the last check may reach the exec. These tests stop at
// the guards (404 / 405 / 409), so nothing is ever actually executed.
func TestHandleUpdatesApplyValidation(t *testing.T) {
	s := &uiServer{repo: t.TempDir()}
	s.setUpdateStatuses([]imageStatus{
		{Container: "vaultwarden", Image: "vaultwarden/server:1.37.0", Stale: true, Newer: "1.37.1"},
		{Container: "it-tools", Image: "ghcr.io/corentinth/it-tools:2024.10.22", State: "up to date"},
	})

	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/admin/updates/apply", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleUpdatesApply(w, r)
		return w
	}

	if w := post("name=caddy; rm -rf /"); w.Code != 404 {
		t.Errorf("arbitrary name must be rejected, got %d", w.Code)
	}
	if w := post("name=it-tools"); w.Code != 404 {
		t.Errorf("up-to-date app has nothing to apply, got %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/admin/updates/apply", nil)
	w := httptest.NewRecorder()
	s.handleUpdatesApply(w, r)
	if w.Code != 405 {
		t.Errorf("GET must be rejected, got %d", w.Code)
	}
	// A valid name past the guards must still be turned away while a backup or
	// restore holds the op lock — locked here to stop the test short of exec.
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if w := post("name=vaultwarden"); w.Code != 409 {
		t.Errorf("op-locked apply should 409, got %d", w.Code)
	}
	if w := post("name=all"); w.Code != 409 {
		t.Errorf("op-locked apply-all should 409, got %d", w.Code)
	}
}

// The page handler must render both the never-checked and checked states.
func TestHandleUpdatesPage(t *testing.T) {
	s := &uiServer{repo: t.TempDir()}
	w := httptest.NewRecorder()
	s.handleUpdatesPage(w, httptest.NewRequest("GET", "/admin/updates", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "No check has run yet") {
		t.Errorf("unchecked page wrong: %d", w.Code)
	}

	s.setUpdateStatuses([]imageStatus{
		{Container: "nextcloud-db", Image: "postgres:16-alpine", State: "NEWER RELEASE 18-alpine (pinned to 16-alpine)", Stale: true, Newer: "18-alpine"},
	})
	w = httptest.NewRecorder()
	s.handleUpdatesPage(w, httptest.NewRequest("GET", "/admin/updates", nil))
	body := w.Body.String()
	if !strings.Contains(body, "major upgrade") {
		t.Errorf("postgres 16->18 must be flagged as major:\n%s", body)
	}
	if strings.Contains(body, "Apply all routine updates") {
		t.Error("a majors-only result must not offer apply-all")
	}
}
