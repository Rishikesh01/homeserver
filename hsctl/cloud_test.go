package main

import (
	"path/filepath"
	"testing"
)

func TestMockProviderLifecycle(t *testing.T) {
	repo := t.TempDir()
	p := &mockProvider{repo: repo}

	// Nothing provisioned yet.
	if _, ok, err := p.FindByName("homeserver-cloud"); err != nil || ok {
		t.Fatalf("expected no VM, got ok=%v err=%v", ok, err)
	}

	vm, err := p.CreateVM(VMSpec{Name: "homeserver-cloud", Tag: "trip-1"})
	if err != nil {
		t.Fatal(err)
	}
	if vm.ID == "" || vm.Name != "homeserver-cloud" || vm.Tag != "trip-1" {
		t.Fatalf("bad VM: %+v", vm)
	}

	// Find it (idempotency support).
	got, ok, err := p.FindByName("homeserver-cloud")
	if err != nil || !ok || got.ID != vm.ID {
		t.Fatalf("FindByName: ok=%v got=%+v err=%v", ok, got, err)
	}

	// Destroy is idempotent: once removes it, twice is a no-op success.
	if err := p.DestroyVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.DestroyVM(vm.ID); err != nil {
		t.Errorf("second destroy should be a no-op, got: %v", err)
	}
	if _, ok, _ := p.FindByName("homeserver-cloud"); ok {
		t.Error("VM should be gone after destroy")
	}
}

func TestNewProvider(t *testing.T) {
	repo := t.TempDir()
	if p, err := newProvider("mock", cloudCfg{}, repo); err != nil || p.Name() != "mock" {
		t.Errorf("mock: %v %v", p, err)
	}
	if p, err := newProvider("", cloudCfg{Provider: "mock"}, repo); err != nil || p == nil {
		t.Errorf("empty should fall back to cfg.Provider: %v", err)
	}
	for _, name := range []string{"hetzner", "digitalocean", "aws"} {
		if _, err := newProvider(name, cloudCfg{}, repo); err == nil {
			t.Errorf("%s should report not-implemented for now", name)
		}
	}
	if _, err := newProvider("frobnicate", cloudCfg{}, repo); err == nil {
		t.Error("unknown provider should error")
	}
}

func TestLoadCloudCfgDefaults(t *testing.T) {
	// Absent cloud.conf => mock provider default.
	if c := loadCloudCfg(t.TempDir()); c.Provider != "mock" {
		t.Errorf("absent cloud.conf should default to mock, got %q", c.Provider)
	}
	// Parsed values come through; tokens are read.
	repo := t.TempDir()
	conf := "PROVIDER=hetzner\nREGION=hel1\nSIZE=cpx21\nHETZNER_TOKEN=secret123\nTAILSCALE_HOST=cloudbox\n"
	writeFile0600(filepath.Join(repo, cloudConfFile), conf)
	c := loadCloudCfg(repo)
	if c.Provider != "hetzner" || c.Region != "hel1" || c.Size != "cpx21" ||
		c.HetznerToken != "secret123" || c.TailscaleHost != "cloudbox" {
		t.Errorf("cloud.conf not parsed: %+v", c)
	}
}
