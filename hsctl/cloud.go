package main

// Pluggable cloud-provider abstraction for on-demand migration. The migration flow is written
// once against the CloudProvider interface; concrete providers (Hetzner, DigitalOcean, AWS)
// plug in behind it, and a local mock provider lets the entire orchestration be exercised and
// tested with no network, no credentials, and no spend.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// VMSpec describes a VM to provision, provider-agnostically.
type VMSpec struct {
	Name      string // stable name for find/idempotency, e.g. "homeserver-cloud"
	Tag       string // per-trip UUID, set as a provider tag/label to verify identity later
	Region    string // provider-specific region/location slug
	Size      string // provider-specific size slug
	Image     string // provider-specific image/AMI id
	SSHPubKey string // public key authorised on the VM (home drives it over SSH/Tailscale)
	CloudInit string // user-data / cloud-init
}

// VM is a provisioned machine.
type VM struct {
	ID   string `json:"id"`
	IPv4 string `json:"ipv4"`
	Name string `json:"name"`
	Tag  string `json:"tag"`
}

// CloudProvider provisions and destroys on-demand VMs. Implementations: mock, hetzner,
// digitalocean, aws. Everything downstream of provisioning (Tailscale, restic, hostname fixup,
// authority handoff) is provider-independent.
type CloudProvider interface {
	Name() string
	CreateVM(spec VMSpec) (VM, error)
	DestroyVM(id string) error
	// FindByName supports idempotency (don't double-provision) and leak detection (a VM left
	// running across trips). Returns ok=false when nothing matches.
	FindByName(name string) (vm VM, ok bool, err error)
}

// cloudCfg is the migration/provider configuration (cloud.conf, gitignored). Tokens are read
// but never logged.
type cloudCfg struct {
	Provider         string // default provider for `migrate`: mock | hetzner | digitalocean | aws
	Region           string
	Size             string
	Image            string
	HetznerToken     string
	DOToken          string
	AWSAccessKey     string
	AWSSecretKey     string
	AWSRegion        string
	TailscaleAuthKey string
	TailscaleHost    string // pinned MagicDNS hostname for the cloud VM (stable cert SAN/VW_DOMAIN)
	HomeSSHRepo      string // sftp: repo URL back to the home HDD over the tailnet
	SSHKeyPath       string // private key the cloud VM uses to reach the home repo
}

const cloudConfFile = "cloud.conf"

// loadCloudCfg reads cloud.conf if present; an absent file yields the mock-provider default so
// the flow is usable out of the box for local testing.
func loadCloudCfg(repo string) cloudCfg {
	c := cloudCfg{Provider: "mock"}
	kv, err := readKV(filepath.Join(repo, cloudConfFile))
	if err != nil {
		return c
	}
	get := func(k, def string) string {
		if v, ok := kv[k]; ok && v != "" {
			return v
		}
		return def
	}
	c.Provider = get("PROVIDER", c.Provider)
	c.Region = get("REGION", "")
	c.Size = get("SIZE", "")
	c.Image = get("IMAGE", "")
	c.HetznerToken = get("HETZNER_TOKEN", "")
	c.DOToken = get("DO_TOKEN", "")
	c.AWSAccessKey = get("AWS_ACCESS_KEY_ID", "")
	c.AWSSecretKey = get("AWS_SECRET_ACCESS_KEY", "")
	c.AWSRegion = get("AWS_REGION", "")
	c.TailscaleAuthKey = get("TAILSCALE_AUTHKEY", "")
	c.TailscaleHost = get("TAILSCALE_HOST", "")
	c.HomeSSHRepo = get("HOME_SSH_REPO", "")
	c.SSHKeyPath = get("SSH_KEY_PATH", "")
	return c
}

// newProvider builds the provider named by `name` (falling back to cfg.Provider, then mock).
// Only the mock is wired up today; the real providers land one per commit in Phase 5.
func newProvider(name string, cfg cloudCfg, repo string) (CloudProvider, error) {
	if name == "" {
		name = cfg.Provider
	}
	switch name {
	case "mock", "":
		return &mockProvider{repo: repo}, nil
	case "hetzner":
		return nil, fmt.Errorf("provider %q is not implemented yet (Phase 5)", name)
	case "digitalocean", "do":
		return nil, fmt.Errorf("provider %q is not implemented yet (Phase 5)", name)
	case "aws":
		return nil, fmt.Errorf("provider %q is not implemented yet (Phase 5)", name)
	default:
		return nil, fmt.Errorf("unknown cloud provider %q (want mock|hetzner|digitalocean|aws)", name)
	}
}

// mockProvider simulates a provider entirely locally — no network, no real VM. It records
// "provisioned" VMs in cloud/mock-vms.json under the repo so the migrate flow's provisioning,
// idempotency, and teardown can be exercised and asserted without spending money.
type mockProvider struct{ repo string }

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) file() string { return filepath.Join(m.repo, "cloud", "mock-vms.json") }

func (m *mockProvider) load() ([]VM, error) {
	b, err := os.ReadFile(m.file())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var vms []VM
	if err := json.Unmarshal(b, &vms); err != nil {
		return nil, err
	}
	return vms, nil
}

func (m *mockProvider) save(vms []VM) error {
	if err := os.MkdirAll(filepath.Dir(m.file()), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(vms, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.file(), append(b, '\n'), 0600)
}

func (m *mockProvider) CreateVM(spec VMSpec) (VM, error) {
	vms, err := m.load()
	if err != nil {
		return VM{}, err
	}
	vm := VM{ID: "mock-" + genPassword(10), IPv4: "127.0.0.1", Name: spec.Name, Tag: spec.Tag}
	return vm, m.save(append(vms, vm))
}

func (m *mockProvider) FindByName(name string) (VM, bool, error) {
	vms, err := m.load()
	if err != nil {
		return VM{}, false, err
	}
	for _, v := range vms {
		if v.Name == name {
			return v, true, nil
		}
	}
	return VM{}, false, nil
}

func (m *mockProvider) DestroyVM(id string) error {
	vms, err := m.load()
	if err != nil {
		return err
	}
	kept := make([]VM, 0, len(vms))
	found := false
	for _, v := range vms {
		if v.ID == id {
			found = true
			continue
		}
		kept = append(kept, v)
	}
	if !found {
		return nil // idempotent: already gone (mirrors a real provider's 404-as-success)
	}
	return m.save(kept)
}
