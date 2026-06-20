# Cloud migration — design

On-demand migration of the homeserver stack **between the home box and a cloud VM**, so the
family can reach their passwords (Vaultwarden) and files (Nextcloud) while travelling, then bring
everything home. This document is the *why* and the *how we get around the hard parts* — the
considerations, the challenges, and the mitigations. It complements the code on the
`feat/cloud-migration` branch.

Audience: whoever maintains this (it's developer-facing, unlike README/ONBOARDING). Keep it
honest — if something isn't proven yet, it says so.

---

## 1. Goal

While traveling, on demand:

1. **Move to cloud** — capture the live stack, spin up a cloud VM, restore the data there, and
   serve it over the internet. The home box may then be powered off.
2. **Move back** — capture whatever changed in the cloud, restore it home, and destroy the VM
   so it stops costing money.

Only **Vaultwarden** (passwords, SQLite) and **Nextcloud** (files, Postgres) carry data worth
migrating. Pi-hole / the web tools are stateless or home-LAN-only.

Non-negotiable: this moves a family's **passwords and files**. Data loss or lockout is
unacceptable, so the design is built around fail-closed safety, not convenience.

---

## 2. Locked decisions (and why)

| Decision | Choice | Why |
|---|---|---|
| Direction | Both ways, on demand | The stated goal; home can be off while away. |
| Reachability | **Tailscale** (private VPN) | No public ports, no domain, no Let's Encrypt; the existing internal Caddy CA keeps working over the tailnet. Smallest attack surface — right for a family. |
| Cloud host | **On-demand, multi-provider** (Hetzner / AWS / DigitalOcean) | Pay only while travelling. A pluggable provider interface, not one vendor. |
| Storage / transport | **One restic repo at home** (the backup HDD), reached by the cloud over **`sftp:` across Tailscale**. No object storage. | Owner wants the SQLite + Postgres DBs to stay the source of truth; reuses the proven restic machinery. |
| Authority | **Stop home; cloud is sole authority while live** | Exactly one editable copy at a time — no split-brain. |
| "Backup from cloud" | The **move-back capture step** (when home is on), not a continuous job | Home can stay fully off during travel. |

These are settled. Everything below follows from them.

---

## 3. End-to-end flow

**Move to cloud** (driven from the home box):

```
guard (home authoritative?) → capture home (strict) → provision VM (record id immediately)
→ seed cloud (restore + hostname fixup + up, over SSH) → PROBE cloud health
→ commit authority=cloud → seal home (down)
```
Any failure *before* the commit ⇒ destroy the VM, home stays live + authoritative.

**Move back** (home powered on again):

```
guard (cloud authoritative) → seal cloud → capture cloud's latest (strict, NO prune)
→ commit authority=home (pin snapshot) → rehydrate home (stage-then-swap restore + fixup + up)
→ verify home at the DATA level → destroy VM → authority=home
```
Idempotent: a crash after the capture resumes from the **pinned** snapshot and never
re-captures the now-sealed cloud.

---

## 4. What we reuse vs. build new

**Reuse** (the data mover already exists):
- `restic` backup/restore — `RESTIC_REPO` already supports `sftp:`; `restore --into-volumes`
  rebuilds every Docker volume by name on a fresh host (`backup.go`).
- Consistency staging — Vaultwarden SQLite `~1s` stop (`stageVaultwardenDB`), Nextcloud
  `pg_dump` (`dumpNextcloudDB`).
- Compose orchestration (`cmdUp`/`cmdDown`), config/secret generation (`Config.Generate`).
- The web admin + auth pattern (`ui.go`).

**Build new**:
- Remote-repo hardening (fail-closed keys, mount-guard bypass, strict/no-prune capture).
- Stage-then-swap restore (don't wipe the only copy).
- Hostname fixup (`fixup.go`).
- Authority state machine + split-brain guard (`migrate.go`).
- Pluggable `CloudProvider` (`cloud.go`) + orchestration + CLI/UI (`migratecmd.go`,
  `migraterun.go`).
- Tailscale + SSH transport (Phase 4, pending).
- Real provider clients (Phase 5, pending).

---

## 5. Considerations, challenges, and how we get around them

This is the heart of it. Each challenge is *why it's hard* → *what we do* → *status*.

### 5.1 Public reachability + TLS (the internal CA can't work on the internet)
**Hard because:** today everything is a LAN IP with an *internal* Caddy CA; that cert is
meaningless on the public internet and a phone on cellular can't even reach a LAN IP.
**Approach:** **Tailscale.** The cloud VM joins the tailnet; the family's devices (already on
the tailnet) reach it at a stable MagicDNS name. Caddy keeps `tls internal` — the internal CA
still validates *over the tailnet* because the devices already trust that root from home. No
public ports, no domain, no Let's Encrypt.
**Status:** design locked; Tailscale bring-up is Phase 4.

### 5.2 Baked-in hostnames (apps remember the *old* origin)
**Hard because:** Vaultwarden bakes `VW_DOMAIN` into passkey/WebAuthn origins + email links;
Nextcloud bakes `trusted_domains` / `overwrite.cli.url` into `config.php`. A restored snapshot
carries the *home* URL, so the app rejects or mis-links on the cloud (and vice-versa).
**Approach:** a post-restore **fixup** (`fixup.go`): rewrite `VW_DOMAIN` (and patch a
panel-saved `config.json` `domain` if present) while the stack is down; **append** the new host
to Nextcloud `trusted_domains` via `occ` while it's up. trusted_domains is *additive* — home and
cloud stay trusted together, so move-back needs no undo. Use a **stable MagicDNS hostname** as
the address so the origin doesn't churn each trip (keeps passkeys/cert SAN stable).
**Status:** **proven** against a real Nextcloud (append + idempotency); unit-tested for the
index/`VW_DOMAIN` logic.

### 5.3 restic against a remote repo (key + mount guard)
**Hard because:** the existing code (a) auto-*generates* a repo key if `.restic-password` is
missing — on a remote repo that mints a NEW key that can't open the existing data; (b) refuses
to run unless a local mount is present (`REQUIRE_MOUNT`), which is meaningless for `sftp:`.
**Approach:** `ensureResticPasswordStrict` **fails closed** on run/restore (never mints; minting
stays only in `backup init`); `isRemoteRepo` makes `requireBackupMount` skip the local-HDD guard
for `sftp:`/`s3:`/`b2:`/… backends.
**Status:** **proven** — negative test (remote + no key → refused) and a real `backup run` over
`sftp:` with the guard bypassed.

### 5.4 Never wipe the only live copy
**Hard because:** the old restore wiped a volume in place, then copied the snapshot in. A failure
mid-copy (disk full, bad snapshot, interrupt) left the *only* live copy half-destroyed.
**Approach:** **stage-then-swap** (`swapDirContents`): move the volume's current entries into a
sibling `<mp>.prev`, copy the snapshot in, and only then drop the staging dir; any failure rolls
the originals back. It moves *child entries* (not the dir node) so Postgres's strict `0700`
data-dir mode is preserved, and it does a free-space preflight.
**Status:** **proven** — `restore --into-volumes` self-test (stale gone, restored present) and
the round-trip suite, 5/5 against real volumes.

### 5.5 Split-brain (two editable copies)
**Hard because:** if home and cloud are both live, the family edits two diverging databases with
no merge path. Restore is one-directional and has no notion of "who is live."
**Approach:** a durable, **fail-closed authority marker** (`migration.state`): exactly one side
is authoritative. `cmdUp` and `backupRun` refuse on the home box unless home is authoritative.
The marker is written atomically (tmp + fsync + rename + dir-fsync); absent ⇒ home (existing
installs unaffected), corrupt ⇒ *unknown* ⇒ refuse. A cross-process `flock` stops two migrations
racing. A `cloud-state` marker flips the guard's meaning on the VM (there, cloud is the
authoritative role).
**Status:** unit-tested (load defaults, atomic round-trip, guard matrix, flock).
**Still to do:** a `hsctl-migrate-enforce` systemd unit that re-seals home on boot, so Docker's
`restart: unless-stopped` can't bypass the guard after a power-cycle.

### 5.6 Losing data created while travelling
**Hard because:** the cloud holds *new* passwords/files; if the move-back capture or the home
restore goes wrong, that data could vanish.
**Approach:** the move-back capture runs **strict** (abort on a torn DB / empty dump, verify the
snapshot after) and **never prunes** (the one repo that is the only path home must not be
pruned). Authority flips to home *after* the capture is safely in the repo, and the home restore
is **pinned** to that exact snapshot and retried idempotently. The VM is destroyed **only after**
home is verified at the data level.
**Status:** orchestration unit-tested (verify-before-destroy, idempotent resume, no-prune mode).
**Accepted residual risk:** with home fully off and no cloud-side backup *during* travel, a VM
that dies mid-trip loses travel-created data. Owner scoped backup to the come-home moment. The
deferred off-site repo (§8) removes even this.

### 5.7 Secrets travelling to an ephemeral, untrusted VM
**Hard because:** a `git clone` is non-runnable (everything is gitignored); the VM needs the
restic key, the `.env`s (incl. the *plaintext* Postgres/Redis passwords that must match
`db-data`), and the SSH key to the home repo. The home security model assumes full-disk LUKS,
which a cloud VM doesn't give. And cloud-init **user-data is readable** via the provider's
metadata/console.
**Approach:** the `.env`s ride *inside* the encrypted restic snapshot (so the consistent
Postgres password travels with its `db-data`). The things that can't (the restic key — it can't
live in the repo it unlocks — and the SSH key) are pushed **home-initiated over SSH/Tailscale
after the VM boots**, to `tmpfs`, never baked into user-data. User-data carries only the
ephemeral, single-use Tailscale auth key. The VM's local disk holding plaintext while running is
minimised by short VM lifetime + destroy-on-return.
**Status:** design locked; the push-over-SSH path is Phase 4.

### 5.8 Cost runaway / a leaked VM
**Hard because:** an on-demand VM that's never destroyed bills forever; a crash mid-provision
could leave an untracked VM.
**Approach:** record the VM id in `migration.state` **immediately** after provision (before
anything else). `FindByName`-based idempotency refuses to double-provision; a per-trip UUID tag
verifies identity (not just name). `destroy` is gated on home being authoritative. Escape hatches:
`migrate force-home` (declare home live when the cloud is gone) and `migrate destroy` (tear down
the recorded VM). A `migrate doctor` cross-provider leak sweep is planned.
**Status:** record-immediately + idempotency + gating + force-home/destroy implemented &
unit-tested (mock provider). `doctor` pending with the real providers.

### 5.9 Long-running ops over a proxy that gets sealed
**Hard because:** `migrate to-cloud` takes Caddy down mid-move — a web request driving it would
drop its own connection. The existing UI pattern is synchronous-redirect with no streaming.
**Approach:** start/end of a migration is **CLI-only** (`hsctl migrate to-cloud` / `back`). The
`/admin/migrate` page shows **status** and the quick, safe controls (force-home, destroy) only;
it never tries to run the long flow over the connection it would kill.
**Status:** implemented (UI status + safe controls; CLI for the heavy flow).

### 5.10 Multiple providers without multiplying the logic
**Hard because:** Hetzner, AWS, and DigitalOcean have different APIs, but the migration logic
must be written once.
**Approach:** a `CloudProvider` interface (`Name`/`CreateVM`/`DestroyVM`/`FindByName`). The
orchestration is provider-agnostic; a provider is ~one file of REST calls (no heavy SDKs). A
**mock** provider (file-backed) lets the whole flow be tested with no network/credentials.
**Status:** interface + mock implemented & unit-tested. Real providers (Hetzner first) are
Phase 5. A **`dockerProvider`** (the "VM" = a dind container) is planned so the *full* round-trip
is testable locally.

### 5.11 Passkeys / 2FA on an origin change
**Hard because:** WebAuthn passkeys are bound to an origin; moving Vaultwarden to a different
host can invalidate them.
**Approach:** use a **stable MagicDNS hostname** as the canonical address so the origin doesn't
change per trip (mostly moots it). Warn-and-continue, and require a TOTP + master-password
fallback before the first trip.
**Status:** policy decided; surfaced as guidance.

---

## 6. Safety invariants (the rules the code must never break)

1. Never auto-generate a restic key on a remote repo / when one is missing on run/restore.
2. Prove the repo opens before acting on it.
3. The move-back capture never prunes (prune is a separate, home-only, post-verify step).
4. Never destroy the cloud until home is verified at the **data** level.
5. Never wipe the only live copy in place (stage-then-swap, roll back on failure).
6. Never seal home until the cloud passes a real health probe.
7. The authority marker fails closed (absent ⇒ home; corrupt ⇒ refuse).
8. The split-brain lock survives a reboot (boot enforcer — pending).
9. One migration at a time (cross-process `flock`).
10. Secrets never go in user-data or the repo key into the repo it unlocks.
11. Record the VM id immediately; gate destroy on home-authoritative.
12. New secret/state files are gitignored before any code lands.

---

## 7. Verification strategy & status

The owner wants **genuine, unfakeable proof** — not claims. Two layers:

**Unit tests (25, no root/docker)** — orchestration sequencing/rollback/idempotency with a fake
steps interface; authority guard matrix; mock provider lifecycle; config parsing; the pure fixup
logic; the stage-then-swap algorithm; template rendering.

**Integration proof — done in a privileged `docker:dind` "lab"** (root inside, fully isolated,
zero risk to the live stack, no host sudo): a real throwaway Vaultwarden + Nextcloud-Postgres
brought up inside, then:
- `hsctl backup verify` → **5/5** incl. the stage-then-swap put-back and WAL preservation;
- remote repo: fail-closed negative test + a real `backup run` over `sftp:` with the mount guard
  bypassed (the exact restic-over-Tailscale transport);
- Nextcloud fixup: `trusted_domains` append + idempotency against a real instance.

**Not yet end-to-end:** a single `migrate to-cloud` → `back` round-trip (needs the Phase-4
cloud-side SSH glue). The plan: a `dockerProvider` whose "VM" is a second dind container, so the
*whole* flow — real passwords + files surviving the trip — is provable locally before any paid
VM. Phase 5 then adds one real cloud round-trip per provider, made unfakeable by a third-node
client probe + a metadata-leak check + console-confirmed teardown.

**The dind lab recipe** (reproducible): `docker run --privileged docker:28-dind`, `git archive`
the repo in, copy a `CGO_ENABLED=0` static `hsctl`, `apk add restic`, `hsctl setup --yes`, bring
the stack up, test.

---

## 8. Status & what's next

| Phase | What | Status |
|---|---|---|
| 0 | Remote-repo hardening, strict/no-prune capture, stage-then-swap restore | **Done, proven** |
| 1 | Hostname fixup | **Done, proven** |
| 2 | Authority state machine + split-brain guard | **Done, unit-tested** |
| 3 | CloudProvider interface + mock + orchestration + CLI + UI | **Done, unit-tested** |
| 4 | Tailscale + SSH transport (seed/probe/seal/capture over the tailnet) | **Pending** — needs a Tailscale auth key + home sftp endpoint |
| 5 | Real providers — Hetzner → DigitalOcean → AWS (REST, not SDKs) | **Pending** — needs provider tokens + a paid VM for the final proof |

**Deferred (after the core is proven):** allow the restic repo to *also* live on an **off-site
target** (a third-party volume or object storage) as a durable off-site copy. Cheap to add —
restic's backend is just a repo string + injected creds on the same plumbing — and it removes the
residual "VM dies mid-trip with home off" risk (§5.6).

**Open items needing the owner:** a tagged/ephemeral Tailscale auth key; which provider to wire
first (recommend Hetzner); OK to spend on one small test VM for the unfakeable end-to-end proof.
