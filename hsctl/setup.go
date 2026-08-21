package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func runSetup(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	yes, _ := f.GetBool("yes")
	force, _ := f.GetBool("force")
	repo := repoDir()
	c := LoadConfig(repo)
	// apply only the flags the user actually set, over the loaded config
	if f.Changed("server-ip") {
		c.ServerIP, _ = f.GetString("server-ip")
	}
	if f.Changed("tz") {
		c.TZ, _ = f.GetString("tz")
	}
	if f.Changed("email") {
		c.ACMEEmail, _ = f.GetString("email")
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
		c = promptConfig(c)
	}
	c.Normalize()
	if err := c.Save(repo); err != nil {
		return err
	}
	fmt.Println("Saved", filepath.Join(repo, confFile))

	secrets, err := c.Generate(repo, force)
	if err != nil {
		return err
	}
	if len(secrets) > 0 {
		fmt.Println("\n========== NEW LOGINS — save these into a password manager ==========")
		for _, s := range secrets {
			fmt.Printf("%-26s %s\n", s.Label, s.Value)
		}
		fmt.Println("=====================================================================")
		fmt.Println("See them again anytime with `hsctl secrets show` (read from the .env files).")
	} else {
		fmt.Println("All .env files already exist (nothing regenerated). Use --force to recreate.")
	}
	fmt.Println("\nNext: hsctl up   then   hsctl get-ca")
	return nil
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

var stdinReader = bufio.NewReader(os.Stdin)

func ask(prompt, def string) string {
	fmt.Printf("  %s [%s]: ", prompt, def)
	line, _ := stdinReader.ReadString('\n')
	if line = strings.TrimSpace(line); line == "" {
		return def
	}
	return line
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

func promptConfig(c Config) Config {
	fmt.Println("== Configure (Enter accepts each [default]) ==")
	c.ServerIP = ask("Server LAN IP", c.ServerIP)
	c.TZ = ask("Timezone", c.TZ)
	c.ACMEEmail = ask("Admin email (Let's Encrypt contact, if you ever use a domain)", c.ACMEEmail)
	c.UIPort = atoiDef(ask("Dashboard (web UI) port", strconv.Itoa(c.UIPort)), c.UIPort)
	// derived defaults follow the IP just entered
	dnsDef := c.PiholeDNSBind
	if dnsDef == "" {
		dnsDef = defaultDNSBind(c.ServerIP)
	}
	c.PiholeDNSBind = ask("Pi-hole DNS bind IP", dnsDef)
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
