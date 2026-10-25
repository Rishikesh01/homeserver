package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // validate timezone names even on a box without system zoneinfo

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func runSetup(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	yes, _ := f.GetBool("yes")
	force, _ := f.GetBool("force")
	if force && !yes {
		if !isTTY() {
			return fmt.Errorf("--force regenerates secrets; to do that non-interactively, add --yes")
		}
		fmt.Println("--force generates NEW secrets: the Vaultwarden /admin token, the Pi-hole and Redis")
		fmt.Println("passwords, and — only if Nextcloud has no data yet — its database and admin passwords.")
		fmt.Println("The old ones stop working. Your app data and other settings are kept.")
		fmt.Print("  Type REGENERATE to continue: ")
		if line, _ := stdinReader.ReadString('\n'); strings.TrimSpace(line) != "REGENERATE" {
			fmt.Println("aborted — nothing was changed")
			return nil
		}
	}
	repo := repoDir()
	// A config file we can't read must stop us here: loading would silently fall back to
	// autodetected values and then save them over the real settings.
	if err := requireReadable(repo, append([]string{confFile}, envFiles...)...); err != nil {
		return err
	}
	c := LoadConfig(repo)
	// What Caddy currently issues certificates for (the autodetected IP on a first setup).
	deployedIP := c.ServerIP
	// apply only the flags the user actually set, over the loaded config
	if f.Changed("server-ip") {
		c.ServerIP, _ = f.GetString("server-ip")
	}
	if f.Changed("tz") {
		c.TZ, _ = f.GetString("tz")
	}
	if f.Changed("pihole-dns-bind") {
		c.PiholeDNSBind, _ = f.GetString("pihole-dns-bind")
	}
	if f.Changed("vw-signups") {
		c.VWSignupsAllowed, _ = f.GetBool("vw-signups")
	}
	if f.Changed("disable-apps") {
		v, _ := f.GetString("disable-apps")
		c.DisabledApps = nil
		for _, d := range splitCSV(v) {
			if !validApp(d) {
				return fmt.Errorf("--disable-apps: unknown app %q — one of: %s", d, strings.Join(appDirs(), ", "))
			}
			c.setDisabled(d, true)
		}
	}

	if !yes && isTTY() {
		c = promptConfig(c, deployedIP)
	} else if c.ServerIP != deployedIP && c.PiholeDNSBind == deployedIP && !f.Changed("pihole-dns-bind") {
		// Pi-hole was bound to the old IP; let Normalize re-derive the bind for the new one.
		c.PiholeDNSBind = ""
	}
	c.Normalize()
	if err := validateConfig(c); err != nil {
		return err
	}
	if !localIPv4(c.ServerIP) {
		fmt.Fprintf(os.Stderr, "warning: %s isn't an address of this machine — the apps will only be "+
			"reachable there if it forwards to this box.\n", c.ServerIP)
	}
	if err := c.Save(repo); err != nil {
		return err
	}
	fmt.Println("Saved", filepath.Join(repo, confFile))

	res, err := c.Generate(repo, force)
	if err != nil {
		return err
	}
	if len(res.Changed) > 0 {
		fmt.Println("\nApplied your changes to the existing config:")
		for _, l := range res.Changed {
			fmt.Println("  " + l)
		}
		if c.ServerIP != deployedIP {
			trustNextcloudIP(repo, c.ServerIP)
		}
	}
	if len(res.Kept) > 0 {
		fmt.Println("\nKept (rotating these would break existing data):")
		for _, k := range res.Kept {
			fmt.Println("  " + k)
		}
		fmt.Println("  For a completely fresh start, delete the data first: hsctl down --volumes")
	}
	if len(res.Secrets) > 0 {
		fmt.Println("\n========== NEW LOGINS — save these into a password manager ==========")
		for _, s := range res.Secrets {
			fmt.Printf("%-26s %s\n", s.Label, s.Value)
		}
		fmt.Println("=====================================================================")
		fmt.Println("See them again anytime with `hsctl secrets show` (read from the .env files) —")
		fmt.Println("except the Vaultwarden /admin token, which is stored hashed: copy it now.")
	} else if len(res.Changed) == 0 {
		fmt.Println("Nothing to change — the existing .env files already match these settings.")
	}
	switch {
	case len(res.Secrets) > 0 && !uiServiceActive():
		fmt.Println("\nNext: hsctl up   then   sudo hsctl install   (runs the dashboard on boot)")
	case len(res.Secrets) > 0 || len(res.Changed) > 0:
		fmt.Println("\nNext: hsctl up   (recreates the containers whose settings changed)")
	}
	return nil
}

// setupArgs rejects positional arguments, with a pointed hint for the classic slip of giving
// a boolean flag its value after a space (`--vw-signups false`), which cobra reads as a stray
// argument.
func setupArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if a := strings.ToLower(args[0]); a == "true" || a == "false" {
		return fmt.Errorf("unexpected %q — give a true/false flag its value with '=', e.g. --vw-signups=%s", args[0], a)
	}
	return fmt.Errorf("unexpected argument %q — setup takes only flags (see hsctl setup --help)", args[0])
}

// trustNextcloudIP adds a new server IP to a running Nextcloud's own trusted_domains: Nextcloud
// only reads NC_TRUSTED_DOMAINS on its first install, so without this an IP change locks it
// behind "access through untrusted domain".
func trustNextcloudIP(repo, ip string) {
	if containerState(repo, "nextcloud-app") != "running" {
		fmt.Println("  (Nextcloud isn't running — `hsctl up` adds the new IP to its trusted domains.)")
		return
	}
	if added, err := ensureNextcloudTrusts(repo, ip); err != nil {
		fmt.Fprintf(os.Stderr, "  could not add %s to Nextcloud's trusted domains yet (%v) — `hsctl up` retries it\n", ip, err)
	} else if added {
		fmt.Printf("  nextcloud: trusted_domains += %s\n", ip)
	}
}

// ensureNextcloudTrusts makes sure ip is in Nextcloud's trusted_domains (via occ), reporting
// whether it had to add it. It errors while Nextcloud isn't installed/ready yet.
func ensureNextcloudTrusts(repo, ip string) (bool, error) {
	occ := func(args ...string) (string, error) {
		return dockerCombined(repo, append([]string{"exec", "-u", "www-data", "nextcloud-app", "php", "occ"}, args...)...)
	}
	out, err := occ("config:system:get", "trusted_domains")
	if err != nil {
		return false, fmt.Errorf("occ: %s", strings.TrimSpace(out))
	}
	domains := strings.Fields(out)
	for _, d := range domains {
		if d == ip {
			return false, nil
		}
	}
	if out, err := occ("config:system:set", "trusted_domains", strconv.Itoa(len(domains)), "--value="+ip); err != nil {
		return false, fmt.Errorf("occ: %s", strings.TrimSpace(out))
	}
	return true, nil
}

// validIPv4 checks s is a dotted IPv4 address (the certificates and binds are IPv4-only).
func validIPv4(s string) error {
	if ip := net.ParseIP(s); ip == nil || ip.To4() == nil {
		return fmt.Errorf("%q isn't an IPv4 address (like 192.168.1.10)", s)
	}
	return nil
}

func validTZ(s string) error {
	if _, err := time.LoadLocation(s); err != nil || s == "" || s == "Local" {
		return fmt.Errorf("%q isn't a timezone name (like Europe/Brussels or Asia/Kolkata)", s)
	}
	return nil
}

func validPort(s string) error {
	if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q isn't a port number (1-65535)", s)
	}
	return nil
}

// validateConfig checks every setting before anything is written. A non-interactive run with a
// bad flag fails here; the prompts re-ask instead.
func validateConfig(c Config) error {
	var errs []string
	add := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	add(validIPv4(c.ServerIP))
	add(validTZ(c.TZ))
	add(validPort(strconv.Itoa(c.UIPort)))
	if c.PiholeDNSBind != "" && c.PiholeDNSBind != c.ServerIP { // same value: already reported
		add(validIPv4(c.PiholeDNSBind))
	}
	for _, d := range c.DisabledApps {
		if !validApp(d) {
			errs = append(errs, fmt.Sprintf("unknown app %q in DISABLED_APPS — one of: %s", d, strings.Join(appDirs(), ", ")))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid settings, nothing was written:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// localIPv4 reports whether ip is assigned to one of this machine's interfaces.
func localIPv4(ip string) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // can't tell — don't warn
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.String() == ip {
			return true
		}
	}
	return false
}

// isTTY reports whether stdin is an interactive terminal — something to ask a question on. (A
// character-device check isn't enough: /dev/null, which jobs and the timer get, is one too.)
func isTTY() bool { return isTerminal(os.Stdin) }

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// composeUpArgs is `compose up -d` plus extra args — without per-layer download progress when
// the output isn't a terminal (a dashboard job's log, the journal), where every progress tick
// would land as its own line and bury the messages that matter.
func composeUpArgs(extra ...string) []string {
	args := []string{"compose", "up", "-d"}
	if !isTerminal(os.Stdout) {
		args = append(args, "--quiet-pull")
	}
	return append(args, extra...)
}

var stdinReader = bufio.NewReader(os.Stdin)

func ask(prompt, def string) string {
	return askValid(prompt, def, nil)
}

// askValid is ask with a check: an answer that fails validate is explained and asked again,
// so a typo never lands in the generated .env files. At end of input it settles for the default
// (validateConfig still checks the final result).
func askValid(prompt, def string, validate func(string) error) string {
	for {
		fmt.Printf("  %s [%s]: ", prompt, def)
		line, err := stdinReader.ReadString('\n')
		if line = strings.TrimSpace(line); line == "" {
			return def
		}
		if validate == nil {
			return line
		}
		verr := validate(line)
		if verr == nil {
			return line
		}
		fmt.Printf("    %v\n", verr)
		if err != nil {
			return def
		}
	}
}

func askYN(prompt string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	fmt.Printf("  %s [%s]: ", prompt, d)
	line, _ := stdinReader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return def
	}
	return strings.HasPrefix(line, "y")
}

// promptConfig asks for each setting. deployedIP is the address the stack runs on now, so a
// Pi-hole bind that followed the old IP follows the new one by default.
func promptConfig(c Config, deployedIP string) Config {
	fmt.Println("== Configure (Enter accepts each [default]) ==")
	c.ServerIP = askValid("Server LAN IP", c.ServerIP, validIPv4)
	c.TZ = askValid("Timezone", c.TZ, validTZ)
	c.UIPort = atoiDef(askValid("Dashboard (web UI) port", strconv.Itoa(c.UIPort), validPort), c.UIPort)
	// derived defaults follow the IP just entered
	dnsDef := c.PiholeDNSBind
	if dnsDef == "" || (dnsDef == deployedIP && c.ServerIP != deployedIP) {
		dnsDef = defaultDNSBind(c.ServerIP)
	}
	c.PiholeDNSBind = askValid("Pi-hole DNS bind IP", dnsDef, validIPv4)
	c.VWSignupsAllowed = askYN("Allow open Vaultwarden signups?", c.VWSignupsAllowed)
	fmt.Printf("  Apps: %s\n", strings.Join(appDirs(), ", "))
	for {
		cur := strings.Join(c.DisabledApps, ",")
		if cur == "" {
			cur = "none"
		}
		ans := ask("Apps to switch OFF (comma-separated, or none)", cur)
		if ans == "none" {
			c.DisabledApps = nil
			break
		}
		c.DisabledApps = nil
		bad := ""
		for _, d := range splitCSV(ans) {
			if !validApp(d) {
				bad = d
				break
			}
			c.setDisabled(d, true)
		}
		if bad == "" {
			break
		}
		fmt.Printf("  unknown app %q\n", bad)
	}
	return c
}
