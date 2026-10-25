package main

import (
	"encoding/json"
	"net/http"
)

// The Command Center: every hsctl CLI subcommand, surfaced in the admin dashboard with a
// plain-language explanation and a Run button, so the whole tool is usable without a
// keyboard on the box. Each entry maps a slug to a FIXED argument vector — the browser
// only ever sends a slug, never raw arguments, so there's no command injection surface:
// the worst a forged POST can do is run one of these vetted commands.
//
// Running a command re-execs THIS binary (os.Executable()) with the entry's args as a background
// job (jobs.go) whose output the page follows. Re-execing reuses all the CLI logic (docker
// handling, restic, the operation lock, etc.) exactly as a terminal run would, and inherits the
// UI process's privileges (root, under systemd) — the same access the commands need.

type danger string

const (
	dangerNone        danger = ""            // read-only or trivially reversible
	dangerCaution     danger = "caution"     // changes state; asks for a click-through
	dangerDestructive danger = "destructive" // can lose data / invalidate logins
)

type webCmd struct {
	Slug      string
	Title     string
	Desc      string   // what it does, in plain language (flags explained inline)
	Category  string   // groups the cards
	Args      []string // hsctl subcommand + flags to exec
	Danger    danger
	Confirm   string // extra confirmation prompt shown before running (when set)
	NeedsRoot bool   // shown as a hint; the systemd UI already runs as root
	Slow      bool   // hint that it may take a while (verify, big backups)
}

// webCmds is the registry. Order within a category is the order shown. Commands that are
// inherently interactive (restore-into-volumes, which demands a typed RESTORE) get their
// own pages instead of living here.
var webCmds = []webCmd{
	// ---- Stack ---------------------------------------------------------------
	{Slug: "up", Title: "Start all services", Category: "Stack",
		Desc: "Start every app and tool (Vaultwarden, Nextcloud, Pi-hole, …) and then Caddy. Safe to run anytime; already-running services are left as they are.",
		Args: []string{"up"}, Danger: dangerNone},
	{Slug: "updates", Title: "Check for app updates", Category: "Stack",
		Desc: "Compare every app's installed image against its registry — see which apps have a newer version available. Read-only; nothing is downloaded or restarted.",
		Args: []string{"updates"}, Danger: dangerNone, Slow: true},
	{Slug: "updates-apply", Title: "Apply routine updates", Category: "Stack",
		Desc: "Download and switch to the newer version of every app that has a routine update, restarting those apps briefly. Major upgrades (e.g. a new Nextcloud or database generation) are skipped and listed — those are applied one at a time from the terminal after a sandbox test.",
		Args: []string{"updates", "--apply", "all", "--yes"}, Danger: dangerCaution, Slow: true,
		Confirm: "Apply all routine app updates now? The updated apps will restart briefly."},
	{Slug: "images", Title: "Check app compatibility", Category: "Stack",
		Desc: "Ask each app's registry which processor types it publishes, and confirm every one of them runs on both 64-bit Intel/AMD and 64-bit ARM (a Raspberry Pi) — including whichever this server is. Read-only; nothing is downloaded.",
		Args: []string{"images"}, Danger: dangerNone, Slow: true},
	{Slug: "status", Title: "Show status", Category: "Stack",
		Desc: "List each container and whether it's running. Read-only.",
		Args: []string{"status"}, Danger: dangerNone},
	{Slug: "restart", Title: "Restart all apps", Category: "Stack",
		Desc: "Stop every app and start the stack again — the fix for an app that's misbehaving. Caddy and this dashboard stay up throughout.",
		Args: []string{"restart"}, Danger: dangerCaution, Confirm: "Restart all apps? They go offline for a minute or so."},
	{Slug: "down", Title: "Stop all apps", Category: "Stack",
		Desc: "Stop every app. Your data is kept, and this dashboard (and the HTTPS proxy in front of it) stays up so you can start them again from here. Stopped apps stay stopped — even after a reboot — until you Start them again.",
		Args: []string{"down", "--keep-proxy"}, Danger: dangerCaution,
		Confirm: "Stop all apps? They stay offline (even after a reboot) until you start them again here."},
	{Slug: "down-volumes", Title: "Stop and DELETE all data", Category: "Stack",
		Desc: "Stop every app AND delete its data — passwords, files, settings. This is how you wipe the stack to start over. There is no undo. The HTTPS proxy keeps running, with its certificate, so this dashboard stays reachable and devices stay trusted.",
		Args: []string{"down", "--volumes", "--keep-proxy"}, Danger: dangerDestructive,
		Confirm: "DELETE ALL DATA? Every app's data volume will be destroyed. This cannot be undone."},

	// ---- Setup & certificates ------------------------------------------------
	{Slug: "setup", Title: "Apply settings / generate missing configs", Category: "Setup",
		Desc: "Apply your saved settings (setup.conf: server IP, timezone, signups, …) to every service config, and create any config (.env) that doesn't exist yet with fresh logins. Existing secrets are never changed. Afterwards, Start all services so the apps pick the changes up.",
		Args: []string{"setup", "--yes"}, Danger: dangerCaution},
	{Slug: "setup-force", Title: "Regenerate secrets", Category: "Setup",
		Desc: "Generate new secrets: the Vaultwarden /admin token and the Pi-hole and Redis passwords (plus Nextcloud's database and admin passwords, but only while Nextcloud has no data yet — those are kept otherwise, because rotating them would lock Nextcloud out of its own database). The new logins appear in the output once; the old ones stop working.",
		Args: []string{"setup", "--yes", "--force"}, Danger: dangerDestructive,
		Confirm: "Regenerate secrets? The current Vaultwarden admin token and Pi-hole password stop working."},
	{Slug: "get-ca", Title: "Export the HTTPS certificate", Category: "Setup",
		Desc: "Write caddy-root-ca.crt — the certificate you install on each phone/laptop so the apps load over HTTPS without warnings. Read-only on the stack.",
		Args: []string{"get-ca"}, Danger: dangerNone},
	{Slug: "install", Title: "Run dashboard on boot", Category: "Setup",
		Desc: "Install this dashboard as a systemd service so it auto-starts when the server boots. Writes a system service file (needs root). On a brand-new install this is also what starts Caddy and opens the setup wizard.",
		Args: []string{"install"}, Danger: dangerCaution, NeedsRoot: true},

	// ---- Backups (the dedicated Backups page has more detail + Restore) -------
	{Slug: "backup-init", Title: "Initialize backup repo", Category: "Backups",
		Desc: "Create the encrypted backup repository at your chosen destination. Run once, after setting a destination. Safe to run again — it just reports the repo already exists.",
		Args: []string{"backup", "init"}, Danger: dangerCaution, NeedsRoot: true},
	{Slug: "backup-run", Title: "Back up now", Category: "Backups",
		Desc: "Take a snapshot: a consistent database dump plus every data volume and config, encrypted and deduplicated. Reads Docker volume files, so it needs root.",
		Args: []string{"backup", "run"}, Danger: dangerNone, NeedsRoot: true, Slow: true},
	{Slug: "backup-list", Title: "List snapshots", Category: "Backups",
		Desc: "Show the snapshots currently in the backup repository. Read-only.",
		Args: []string{"backup", "list"}, Danger: dangerNone},
	{Slug: "backup-forget", Title: "Prune old snapshots", Category: "Backups",
		Desc: "Apply the retention policy (keep N daily/weekly/monthly) and delete + prune everything older. Frees space; old snapshots beyond the policy are gone for good.",
		Args: []string{"backup", "forget"}, Danger: dangerCaution, NeedsRoot: true},
	{Slug: "backup-verify", Title: "Self-test backups", Category: "Backups",
		Desc: "Prove backups actually work end-to-end: back up and restore throwaway data (incl. a real Vaultwarden + Postgres round-trip). Never touches your live data. Takes a minute or two.",
		Args: []string{"backup", "verify"}, Danger: dangerNone, NeedsRoot: true, Slow: true},
	{Slug: "backup-replicate", Title: "Copy backups off-site", Category: "Backups",
		Desc: "Copy every snapshot to the off-site replica now (set it on the Backups page) — every backup also does this on its own. Incremental and encrypted with the same password; the first run creates the replica repo. Needs the primary repo reachable.",
		Args: []string{"backup", "replicate"}, Danger: dangerNone, NeedsRoot: true, Slow: true},

	// ---- Secrets -------------------------------------------------------------
	{Slug: "secrets-show", Title: "Show logins", Category: "Secrets",
		Desc: "Print the generated logins (read from the .env files): Nextcloud, Pi-hole, the dashboard, etc. Admin-only; these are real passwords.",
		Args: []string{"secrets", "show"}, Danger: dangerNone},
	{Slug: "secrets-rotate-vw", Title: "New Vaultwarden admin token", Category: "Secrets",
		Desc: "Generate a fresh Vaultwarden /admin token, store it hashed, and recreate the container. Shown once in the output — save it. The old token stops working.",
		Args: []string{"secrets", "rotate-vw-admin"}, Danger: dangerCaution},
}

// webCmdCategories is the display order of the Command Center sections.
var webCmdCategories = []string{"Stack", "Setup", "Backups", "Secrets"}

// Template helpers — keep the danger logic in Go so templates can't drift from it.
func (c webCmd) IsDestructive() bool { return c.Danger == dangerDestructive }
func (c webCmd) IsCaution() bool     { return c.Danger == dangerCaution }

// BtnClass maps danger to a button colour: red destructive, gray caution, green safe.
func (c webCmd) BtnClass() string {
	switch c.Danger {
	case dangerDestructive:
		return "red"
	case dangerCaution:
		return "gray"
	default:
		return "green"
	}
}

// lookupWebCmd finds a registry entry by slug.
func lookupWebCmd(slug string) (webCmd, bool) {
	for _, c := range webCmds {
		if c.Slug == slug {
			return c, true
		}
	}
	return webCmd{}, false
}

// handleRun starts one registry command as a background job (see jobs.go) and returns its ID as
// JSON; the page then follows the job's output. The page-level confirmation (and Danger styling)
// lives in the template; here we validate the slug and, for destructive commands, the explicit
// confirmation token. POST-only so it isn't triggerable by a plain link/GET.
func (s *uiServer) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	c, ok := lookupWebCmd(r.FormValue("slug"))
	if !ok {
		http.Error(w, "unknown command", http.StatusNotFound)
		return
	}
	// Destructive commands (down-volumes, setup-force) require an explicit confirmation
	// token in the POST. The page prompts the operator and only then sends confirm=<slug>,
	// so a blind or replayed POST without the token is refused here — the data-wipe can't
	// fire on a stray request, not just on a client-side window.confirm.
	if c.Danger == dangerDestructive && r.FormValue("confirm") != c.Slug {
		http.Error(w, "this command is destructive — confirmation required", http.StatusForbidden)
		return
	}
	if j, ok := s.startJob(w, r, c.Slug, c.Title, c.Args); ok {
		writeJobID(w, j)
	}
}

// writeJobID answers a job-starting POST with the new job's ID for the page to follow.
func writeJobID(w http.ResponseWriter, j job) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": j.ID})
}
