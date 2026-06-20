package main

// Migration orchestration: the to-cloud / move-back state machines, and the `hsctl migrate`
// CLI. The side-effecting operations live behind the migrateSteps interface so the orchestration
// — sequencing, commit points, rollback, idempotency — is unit-tested with a fake, independent
// of restic/docker/SSH. The real cloud-side transport (seed/probe/capture over Tailscale+SSH)
// is wired in Phase 4; until then realSteps' cloud-side methods return errPhase4 and `migrate
// to-cloud`/`back` refuse upfront unless the transport is configured, so they never half-run.

import (
	"errors"
	"fmt"
)

const cloudVMName = "homeserver-cloud"

var errPhase4 = errors.New("cloud transport (Tailscale + restic-over-sftp) is not configured yet — see Phase 4; " +
	"set HOME_SSH_REPO, TAILSCALE_AUTHKEY and SSH_KEY_PATH in cloud.conf")

// migrateSteps are the side-effecting operations the orchestrator performs, behind an interface
// for testability. Home-side steps run locally; cloud-side steps run ON the VM over SSH/Tailscale.
type migrateSteps interface {
	adopt(name string) (VM, bool, error) // find an existing VM for this stack (idempotency/leak guard)
	captureHome(tag string) (snapshotID string, err error)
	provision(spec VMSpec) (VM, error)
	seedCloud(vm VM, snapshotID string, id hostIdentity) error // restore + fixup + up ON the cloud
	probeCloud(vm VM, id hostIdentity) error                   // real client-perspective health check
	sealHome() error                                           // cmdDown
	sealCloud(vm VM) error                                     // stop the cloud stack before the final capture
	captureCloud(vm VM) (snapshotID string, err error)         // backup ON the cloud into the home repo
	rehydrateHome(snapshotID string, id hostIdentity) error    // restore into home + fixup + up
	verifyHome(id hostIdentity) error                          // data-level health check at home
	destroy(vm VM) error
}

type migrator struct {
	repo     string
	cfg      Config
	cloud    cloudCfg
	provider CloudProvider
	steps    migrateSteps
	log      func(format string, a ...any)
}

// cloudHost is the address clients use to reach the cloud stack: the pinned MagicDNS hostname
// if set (stable across trips, so VW_DOMAIN/cert SAN don't churn), else the VM's IP/name.
func cloudHost(cfg cloudCfg, vm VM) string {
	switch {
	case cfg.TailscaleHost != "":
		return cfg.TailscaleHost
	case vm.IPv4 != "" && vm.IPv4 != "127.0.0.1":
		return vm.IPv4
	default:
		return vm.Name
	}
}

// toCloud seals home and brings the stack up on a freshly provisioned VM. Safety ordering:
// capture -> provision (record immediately) -> seed -> PROBE; only once the cloud is proven
// healthy do we commit authority=cloud and THEN seal home. Any failure before the commit rolls
// back (destroy the VM) and leaves home live and authoritative.
func (m *migrator) toCloud() error {
	return withMigrate(m.repo, func() error {
		if st := loadState(m.repo); st.Authority != authHome {
			return fmt.Errorf("cannot migrate to cloud: home is not authoritative (authority=%s) — `hsctl migrate status`", st.Authority)
		}
		if vm, ok, err := m.steps.adopt(cloudVMName); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("a cloud VM named %q already exists (id=%s) — `hsctl migrate status` / `hsctl migrate destroy` first", cloudVMName, vm.ID)
		}

		tag := genPassword(12)
		m.log("capturing home snapshot…")
		snap, err := m.steps.captureHome(tag)
		if err != nil {
			return fmt.Errorf("capture home: %w", err)
		}

		m.log("provisioning cloud VM via %s…", m.provider.Name())
		vm, err := m.steps.provision(VMSpec{Name: cloudVMName, Tag: tag, Region: m.cloud.Region, Size: m.cloud.Size, Image: m.cloud.Image})
		if err != nil {
			return fmt.Errorf("provision: %w", err)
		}
		// Record the VM immediately (cost guard: a crash now must not leak an untracked VM).
		m.saveProvisioned(vm, snap, tag)

		id := cloudIdentity(cloudHost(m.cloud, vm))
		m.log("seeding cloud from snapshot…")
		if err := m.steps.seedCloud(vm, snap, id); err != nil {
			return m.rollback(vm, fmt.Errorf("seed cloud: %w", err))
		}
		m.log("probing cloud health…")
		if err := m.steps.probeCloud(vm, id); err != nil {
			return m.rollback(vm, fmt.Errorf("cloud health probe: %w", err))
		}

		// COMMIT: the cloud is healthy. Flip authority, THEN seal home. A crash in the brief
		// window before this commit leaves authority=home (home stays live; the VM is an orphan
		// cleaned up by `migrate destroy`). The cloud address isn't handed out until we return.
		if err := saveState(m.repo, migrationState{Authority: authCloud, Phase: "cloud-live",
			Provider: m.provider.Name(), CloudServerID: vm.ID, CloudTailnetAddr: cloudHost(m.cloud, vm), SnapshotID: snap, VMTag: tag}); err != nil {
			return fmt.Errorf("commit authority=cloud: %w", err)
		}
		m.log("sealing home…")
		if err := m.steps.sealHome(); err != nil {
			return fmt.Errorf("authority is now CLOUD but sealing home failed — stop it manually with `hsctl down`: %w", err)
		}
		m.log("done — the cloud is live at %s", id.VWURL)
		return nil
	})
}

// back re-captures the cloud's latest state into the home repo, rehydrates home, verifies it,
// and only THEN destroys the VM. It is idempotent: once the cloud has been captured (and
// authority flipped to home), a re-run resumes from the pinned snapshot and never re-captures
// from a now-sealed cloud.
func (m *migrator) back() error {
	return withMigrate(m.repo, func() error {
		st := loadState(m.repo)
		if st.Authority == authHome && st.Phase != "move-back:captured" {
			return errors.New("nothing to bring back: home is already authoritative — `hsctl migrate status`")
		}
		if st.Authority == authUnknown {
			return errors.New("migration state is unknown/corrupt — `hsctl migrate status`, then `migrate force-home` if home is truly authoritative")
		}
		vm := VM{ID: st.CloudServerID, Name: st.CloudTailnetAddr, Tag: st.VMTag}
		homeID := homeIdentity(m.cfg)
		snap := st.SnapshotID

		if st.Phase != "move-back:captured" { // not yet captured: seal cloud + capture its latest
			m.log("sealing cloud…")
			if err := m.steps.sealCloud(vm); err != nil {
				return fmt.Errorf("seal cloud: %w", err)
			}
			m.log("capturing the cloud's latest state into the home repo…")
			s, err := m.steps.captureCloud(vm)
			if err != nil {
				return fmt.Errorf("capture cloud: %w", err)
			}
			snap = s
			// COMMIT: the cloud's latest is now at home. Flip authority to home before rehydrating
			// so an interrupted rehydrate is retryable (and never re-captures the sealed cloud).
			if err := saveState(m.repo, migrationState{Authority: authHome, Phase: "move-back:captured",
				Provider: st.Provider, CloudServerID: vm.ID, CloudTailnetAddr: vm.Name, SnapshotID: snap, VMTag: st.VMTag}); err != nil {
				return fmt.Errorf("commit authority=home: %w", err)
			}
		}

		m.log("rehydrating home from snapshot %s…", snap)
		if err := m.steps.rehydrateHome(snap, homeID); err != nil {
			return fmt.Errorf("rehydrate home failed — re-run `hsctl migrate back` to retry (the cloud was NOT destroyed): %w", err)
		}
		m.log("verifying home…")
		if err := m.steps.verifyHome(homeID); err != nil {
			return fmt.Errorf("home verify failed — the cloud was NOT destroyed; investigate then re-run: %w", err)
		}
		m.log("destroying cloud VM…")
		if err := m.steps.destroy(vm); err != nil {
			return fmt.Errorf("home is live, but destroying the cloud VM failed — destroy it manually to stop billing (id=%s): %w", vm.ID, err)
		}
		if err := saveState(m.repo, migrationState{Authority: authHome}); err != nil {
			return err
		}
		m.log("done — home is live.")
		return nil
	})
}

func (m *migrator) saveProvisioned(vm VM, snap, tag string) {
	_ = saveState(m.repo, migrationState{Authority: authHome, Phase: "to-cloud:provisioned",
		Provider: m.provider.Name(), CloudServerID: vm.ID, CloudTailnetAddr: cloudHost(m.cloud, vm), SnapshotID: snap, VMTag: tag})
}

// rollback is the to-cloud failure path BEFORE the authority commit: destroy the orphan VM and
// leave home live + authoritative. The original error is wrapped, not masked.
func (m *migrator) rollback(vm VM, cause error) error {
	if err := m.steps.destroy(vm); err != nil {
		return fmt.Errorf("%w; ALSO failed to destroy the VM (id=%s) — destroy it manually: %v", cause, vm.ID, err)
	}
	_ = saveState(m.repo, migrationState{Authority: authHome, LastError: cause.Error()})
	return fmt.Errorf("%w (rolled back: VM destroyed, home still live)", cause)
}
