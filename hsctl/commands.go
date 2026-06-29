package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// The Command Center: every hsctl CLI subcommand, surfaced in the admin dashboard with a
// plain-language explanation and a Run button, so the whole tool is usable without a
// keyboard on the box. Each entry maps a slug to a FIXED argument vector — the browser
// only ever sends a slug, never raw arguments, so there's no command injection surface:
// the worst a forged POST can do is run one of these vetted commands.
//
// Running a command re-execs THIS binary (os.Executable()) with the entry's args and
// streams the combined output back to the page. Re-execing reuses all the CLI logic
// (sudo/docker handling, restic, etc.) exactly as a terminal run would, and inherits the
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
	{Slug: "status", Title: "Show status", Category: "Stack",
		Desc: "List each container and whether it's running. Read-only.",
		Args: []string{"status"}, Danger: dangerNone},
	{Slug: "down", Title: "Stop all services", Category: "Stack",
		Desc: "Stop every service. The apps go offline until you Start them again; your data is kept. They also come back on their own after a reboot.",
		Args: []string{"down"}, Danger: dangerCaution, Confirm: "Stop all services? The apps will go offline until you start them again."},
	{Slug: "down-volumes", Title: "Stop and DELETE all data", Category: "Stack",
		Desc: "Stop everything AND delete all data volumes — passwords, files, settings. This is how you wipe the stack to start over. There is no undo.",
		Args: []string{"down", "--volumes"}, Danger: dangerDestructive,
		Confirm: "DELETE ALL DATA? Every service's data volume will be destroyed. This cannot be undone."},

	// ---- Setup & certificates ------------------------------------------------
	{Slug: "setup", Title: "Generate missing configs", Category: "Setup",
		Desc: "Create any service config (.env) that doesn't exist yet, from your saved settings, and generate logins for new services. Existing configs and secrets are left untouched.",
		Args: []string{"setup", "--yes"}, Danger: dangerCaution},
	{Slug: "setup-force", Title: "Regenerate ALL secrets", Category: "Setup",
		Desc: "Rebuild every service config and generate brand-new passwords/secrets for ALL services. Destructive: current logins stop working and data tied to old secrets can become unreadable. Only for a fresh start.",
		Args: []string{"setup", "--yes", "--force"}, Danger: dangerDestructive,
		Confirm: "Regenerate ALL secrets? Current logins will stop working. Only do this for a clean re-setup."},
	{Slug: "get-ca", Title: "Export the HTTPS certificate", Category: "Setup",
		Desc: "Write caddy-root-ca.crt — the certificate you install on each phone/laptop so the apps load over HTTPS without warnings. Read-only on the stack.",
		Args: []string{"get-ca"}, Danger: dangerNone},
	{Slug: "install", Title: "Run dashboard on boot", Category: "Setup",
		Desc: "Install this dashboard as a systemd service so it auto-starts when the server boots. Writes a system service file (needs root).",
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

// flushWriter writes to the HTTP response and flushes after each chunk, so the browser
// sees command output live instead of buffered until the end.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

// handleRun streams one registry command's combined output back to the browser as it
// runs. The page-level confirmation (and Danger styling) lives in the template; here we
// only validate the slug is real and then exec. POST-only so it isn't triggerable by a
// plain link/GET.
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
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	fl, _ := w.(http.Flusher)
	fw := flushWriter{w: w, f: fl}

	fmt.Fprintf(fw, "$ hsctl %s\n\n", strings.Join(c.Args, " "))
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(fw, "cannot locate hsctl binary: %v\n", err)
		return
	}
	cmd := exec.Command(exe, c.Args...)
	cmd.Dir = s.repo
	cmd.Env = append(os.Environ(), "HOMESERVER_DIR="+s.repo)
	// Same writer for both streams: os/exec then serialises the two, so interleaved
	// stdout/stderr stay coherent without a mutex.
	cmd.Stdout, cmd.Stderr = fw, fw
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(fw, "\n[command exited with error: %v]\n", err)
		return
	}
	fmt.Fprintf(fw, "\n[done]\n")
}
