package main

// realSteps wires the migrateSteps interface to the actual backup/restore/fixup/provider
// operations, plus the `hsctl migrate` CLI. The cloud-side steps (seed/probe/seal/capture ON
// the VM) require the Tailscale + restic-over-sftp transport landed in Phase 4; until then they
// return errPhase4 and the CLI refuses to start a real migration unless the transport is
// configured.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

type realSteps struct {
	repo     string
	cfg      Config
	bcfg     backupCfg
	cloud    cloudCfg
	provider CloudProvider
}

func (s realSteps) adopt(name string) (VM, bool, error) { return s.provider.FindByName(name) }

func (s realSteps) captureHome(tag string) (string, error) {
	if err := backupRunWith(s.repo, s.bcfg, backupRunOpts{strict: true}); err != nil {
		return "", err
	}
	return latestSnapshotID(s.repo, s.bcfg)
}

func (s realSteps) provision(spec VMSpec) (VM, error) { return s.provider.CreateVM(spec) }

func (s realSteps) sealHome() error { return cmdDown(false) }

func (s realSteps) rehydrateHome(snapshotID string, id hostIdentity) error {
	// Restore the cloud's snapshot into home's volumes (cmdDown -> stage-then-swap -> cmdUp;
	// authority is already home here so cmdUp's guard passes), then ensure home stays trusted.
	// VW_DOMAIN is unaffected — restore touches volumes, not the repo's .env (home's stays home).
	if err := restoreSnapshotIntoVolumes(s.repo, s.bcfg, snapshotID); err != nil {
		return err
	}
	return applyNextcloudFixup(s.repo, id)
}

func (s realSteps) verifyHome(id hostIdentity) error {
	if err := waitNextcloudReady(s.repo, 120*time.Second); err != nil {
		return fmt.Errorf("nextcloud did not come back: %w", err)
	}
	if st := containerState(s.repo, "vaultwarden"); st != "running" {
		return fmt.Errorf("vaultwarden is not running after restore (state=%q)", st)
	}
	return nil
}

func (s realSteps) destroy(vm VM) error { return s.provider.DestroyVM(vm.ID) }

// Cloud-side steps run ON the VM over SSH/Tailscale — wired in Phase 4.
func (s realSteps) seedCloud(VM, string, hostIdentity) error { return errPhase4 }
func (s realSteps) probeCloud(VM, hostIdentity) error        { return errPhase4 }
func (s realSteps) sealCloud(VM) error                       { return errPhase4 }
func (s realSteps) captureCloud(VM) (string, error)          { return "", errPhase4 }

// latestSnapshotID returns the short id of the most recent homeserver-tagged snapshot, so a
// move-back can pin and idempotently restore exactly that one (never a moving "latest").
func latestSnapshotID(repo string, cfg backupCfg) (string, error) {
	out, err := resticOutput(repo, cfg, "snapshots", "--latest", "1", "--tag", "homeserver", "--json")
	if err != nil {
		return "", fmt.Errorf("list snapshots: %w\n%s", err, out)
	}
	var snaps []struct {
		ShortID string `json:"short_id"`
	}
	if err := json.Unmarshal([]byte(out), &snaps); err != nil {
		return "", fmt.Errorf("parse snapshots json: %w", err)
	}
	if len(snaps) == 0 {
		return "", errors.New("no homeserver snapshot found after backup")
	}
	return snaps[len(snaps)-1].ShortID, nil
}

// transportReady reports whether the Phase-4 cloud transport is configured. A real migration
// refuses unless it is, so to-cloud/back never half-run on an unconfigured box.
func transportReady(c cloudCfg) error {
	if c.Provider == "mock" {
		return nil // the mock needs no transport; used only by the orchestration smoke path
	}
	if c.HomeSSHRepo == "" || c.TailscaleAuthKey == "" || c.SSHKeyPath == "" {
		return errPhase4
	}
	return nil
}

func buildMigrator(providerName string) (*migrator, error) {
	repo, err := requireRepoDir()
	if err != nil {
		return nil, err
	}
	cloud := loadCloudCfg(repo)
	if providerName == "" {
		providerName = cloud.Provider
	}
	p, err := newProvider(providerName, cloud, repo)
	if err != nil {
		return nil, err
	}
	cfg := LoadConfig(repo)
	cfg.Normalize()
	bcfg := loadBackupCfg(repo)
	return &migrator{
		repo: repo, cfg: cfg, cloud: cloud, provider: p,
		steps: realSteps{repo: repo, cfg: cfg, bcfg: bcfg, cloud: cloud, provider: p},
		log:   func(f string, a ...any) { fmt.Printf("• "+f+"\n", a...) },
	}, nil
}

func migrateCmd() *cobra.Command {
	root := &cobra.Command{Use: "migrate", Short: "Move the stack between home and an on-demand cloud VM"}

	status := &cobra.Command{Use: "status", Short: "Show who is authoritative + any cloud VM", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runMigrateStatus() }}

	toCloud := &cobra.Command{Use: "to-cloud", Short: "Seal home and bring the stack up on a fresh cloud VM", Args: cobra.NoArgs,
		RunE: runMigrateToCloud}
	toCloud.Flags().String("provider", "", "cloud provider (default: cloud.conf PROVIDER)")
	toCloud.Flags().Bool("yes", false, "skip the confirmation prompt")

	back := &cobra.Command{Use: "back", Short: "Capture the cloud, rehydrate home, then destroy the VM", Args: cobra.NoArgs,
		RunE: runMigrateBack}
	back.Flags().Bool("yes", false, "skip the confirmation prompt")

	forceHome := &cobra.Command{Use: "force-home", Short: "Declare home authoritative (escape hatch when the cloud is gone)", Args: cobra.NoArgs,
		RunE: runMigrateForceHome}
	forceHome.Flags().Bool("yes", false, "skip the confirmation prompt")

	destroy := &cobra.Command{Use: "destroy", Short: "Destroy the recorded cloud VM (cost guard / cleanup)", Args: cobra.NoArgs,
		RunE: runMigrateDestroy}
	destroy.Flags().Bool("yes", false, "skip the confirmation prompt")

	root.AddCommand(status, toCloud, back, forceHome, destroy)
	return root
}

func runMigrateStatus() error {
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	st := loadState(repo)
	fmt.Printf("authority:   %s\n", st.Authority)
	if st.Phase != "" {
		fmt.Printf("phase:       %s\n", st.Phase)
	}
	if st.Provider != "" {
		fmt.Printf("provider:    %s\n", st.Provider)
	}
	if st.CloudServerID != "" {
		fmt.Printf("cloud VM:    id=%s addr=%s\n", st.CloudServerID, st.CloudTailnetAddr)
	}
	if st.SnapshotID != "" {
		fmt.Printf("snapshot:    %s\n", st.SnapshotID)
	}
	if st.AuthorityAt != "" {
		fmt.Printf("since:       %s\n", st.AuthorityAt)
	}
	if st.LastError != "" {
		fmt.Printf("last error:  %s\n", st.LastError)
	}
	switch st.Authority {
	case authCloud:
		fmt.Println("\nThe CLOUD is live. Bring it home with: hsctl migrate back")
	case authUnknown:
		fmt.Println("\nState is UNKNOWN — home ops are blocked. If home is truly authoritative: hsctl migrate force-home")
	default:
		fmt.Println("\nHome is live.")
	}
	return nil
}

func runMigrateToCloud(cmd *cobra.Command, _ []string) error {
	provider, _ := cmd.Flags().GetString("provider")
	yes, _ := cmd.Flags().GetBool("yes")
	m, err := buildMigrator(provider)
	if err != nil {
		return err
	}
	if err := transportReady(m.cloud); err != nil {
		return err
	}
	if !yes && !askYN("This will SEAL home and move the live stack to the cloud. Proceed?", false) {
		return errors.New("aborted")
	}
	return m.toCloud()
}

func runMigrateBack(cmd *cobra.Command, _ []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	m, err := buildMigrator("")
	if err != nil {
		return err
	}
	if !yes && !askYN("This will capture the cloud, rehydrate home, then DESTROY the cloud VM. Proceed?", false) {
		return errors.New("aborted")
	}
	return m.back()
}

func runMigrateForceHome(cmd *cobra.Command, _ []string) error {
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	st := loadState(repo)
	if st.Authority == authHome {
		fmt.Println("home is already authoritative — nothing to do")
		return nil
	}
	fmt.Printf("Current authority: %s (cloud VM id=%s addr=%s).\n", st.Authority, st.CloudServerID, st.CloudTailnetAddr)
	fmt.Println("force-home declares HOME authoritative WITHOUT capturing the cloud — any data created on")
	fmt.Println("the cloud since the last capture is NOT brought back. Use this only if the cloud is gone.")
	if !yes && !askYN("Declare home authoritative anyway?", false) {
		return errors.New("aborted")
	}
	if err := saveState(repo, migrationState{Authority: authHome, LastError: "forced home via `migrate force-home`"}); err != nil {
		return err
	}
	fmt.Println("home is now authoritative. If a cloud VM is still running, destroy it: hsctl migrate destroy")
	return nil
}

func runMigrateDestroy(cmd *cobra.Command, _ []string) error {
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	st := loadState(repo)
	if st.CloudServerID == "" {
		fmt.Println("no cloud VM recorded — nothing to destroy")
		return nil
	}
	if st.Authority == authCloud {
		return errors.New("refusing: the cloud is still authoritative — bring it home first (`hsctl migrate back`), or `migrate force-home` if it's truly gone")
	}
	p, err := newProvider(st.Provider, loadCloudCfg(repo), repo)
	if err != nil {
		return err
	}
	if !yes && !askYN(fmt.Sprintf("Destroy cloud VM id=%s (%s)?", st.CloudServerID, st.Provider), false) {
		return errors.New("aborted")
	}
	if err := p.DestroyVM(st.CloudServerID); err != nil {
		return fmt.Errorf("destroy VM: %w", err)
	}
	st.CloudServerID, st.CloudTailnetAddr, st.Provider, st.VMTag, st.Phase = "", "", "", "", ""
	if err := saveState(repo, st); err != nil {
		return err
	}
	fmt.Println("cloud VM destroyed.")
	return nil
}
