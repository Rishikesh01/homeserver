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
	before := c
	before.Normalize()
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
	if f.Changed("domain") {
		c.Domain, _ = f.GetString("domain")
	}
	if f.Changed("letsencrypt") {
		c.LetsEncrypt, _ = f.GetBool("letsencrypt")
	}
	if f.Changed("acme-challenge") {
		c.ACMEChallenge, _ = f.GetString("acme-challenge")
	}
	if f.Changed("dns-provider") {
		c.DNSProvider, _ = f.GetString("dns-provider")
	}
	if f.Changed("dns-token") {
		c.DNSToken, _ = f.GetString("dns-token")
	} else if v := os.Getenv("ACME_DNS_TOKEN"); v != "" {
		c.DNSToken = v
	}
	if f.Changed("acme-staging") {
		c.ACMEStaging, _ = f.GetBool("acme-staging")
	}

	if !yes && isTTY() {
		c = promptConfig(c)
	}
	c.Normalize()
	if err := validateTLS(c); err != nil {
		return fmt.Errorf("Let's Encrypt settings: %w", err)
	}
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
	// The domain settings changed: on a running stack, apply them now (same as `hsctl letsencrypt
	// enable`); otherwise line the app configs up and let `hsctl up` do the rest. An unchanged
	// re-run (e.g. the dashboard's "Generate missing configs") touches nothing here.
	if leStateChanged(before, c) {
		if containerState(repo, "caddy") != "" {
			fmt.Println("\n== applying the domain settings to the running stack ==")
			if err := applyTLS(repo, c, applyOpts{}, os.Stdout); err != nil {
				return err
			}
		} else {
			if _, err := syncAppEnvs(repo, c, leHostsFor(c, LoadServices(repo)), os.Stdout); err != nil {
				return err
			}
			if c.leEnabled() {
				fmt.Printf("\nLet's Encrypt is on for %s (%s challenge). `hsctl up` applies it", c.Domain, c.ACMEChallenge)
				if c.leUsesDNS() {
					fmt.Printf(" — the first run builds Caddy with the %s plugin (a few minutes)", c.DNSProvider)
				}
				fmt.Println(".")
				fmt.Println("Then: hsctl letsencrypt status   (shows the certificates + the DNS names to point at this box)")
			}
		}
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
	c.ACMEEmail = ask("Admin email (the Let's Encrypt contact, if you use a domain)", c.ACMEEmail)
	c.UIPort = atoiDef(ask("Dashboard (web UI) port", strconv.Itoa(c.UIPort)), c.UIPort)
	// derived defaults follow the IP just entered
	dnsDef := c.PiholeDNSBind
	if dnsDef == "" {
		dnsDef = defaultDNSBind(c.ServerIP)
	}
	c.PiholeDNSBind = ask("Pi-hole DNS bind IP", dnsDef)
	c.VWSignupsAllowed = askYN("Allow open Vaultwarden signups?", c.VWSignupsAllowed)

	fmt.Println("\n  Optional: also serve the apps on a public domain you own, with Let's Encrypt certificates")
	fmt.Println("  (https://vault.<domain> etc. — nothing to install on devices). The default 'dns' challenge")
	fmt.Println("  keeps the box LAN-only: it needs your DNS provider's API token instead of port-forwarding.")
	c.LetsEncrypt = askYN("Use a public domain with Let's Encrypt?", c.LetsEncrypt)
	if c.LetsEncrypt {
		c.Domain = ask("Domain (apps become vault.<domain>, cloud.<domain>, …)", c.Domain)
		if c.ACMEEmail == "you@example.com" {
			c.ACMEEmail = ask("Contact email for Let's Encrypt (a real one)", "")
		}
		if c.ACMEChallenge == "" {
			c.ACMEChallenge = challengeDNS
		}
		c.ACMEChallenge = ask("Challenge: dns (LAN-only, DNS API token) or http (port 80 forwarded)", c.ACMEChallenge)
		if c.ACMEChallenge == challengeDNS {
			if c.DNSProvider == "" {
				c.DNSProvider = "cloudflare"
			}
			c.DNSProvider = ask("DNS provider plugin ("+strings.Join(dnsProviders[:6], ", ")+", …)", c.DNSProvider)
			def := ""
			if c.DNSToken != "" {
				def = "(keep the saved token)"
			}
			if v := ask(c.DNSProvider+" API token (input is echoed)", def); v != def {
				c.DNSToken = v
			}
		}
		c.ACMEStaging = askYN("Use Let's Encrypt's STAGING CA first? (untrusted test certs, no rate limits)", c.ACMEStaging)
	}
	return c
}
