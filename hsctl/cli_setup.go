package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/setup"
	"github.com/spf13/cobra"
)

func (a *app) setupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure and generate each service's .env, or apply changed settings (interactive)",
		Args:  setupArgs,
		RunE:  a.inRepo(a.locked("setup", 0, a.runSetup)),
	}
	cmd.Flags().Bool("yes", false, "non-interactive: accept defaults/setup.conf/flags")
	cmd.Flags().Bool("force", false, "regenerate secrets (asks you to type REGENERATE; keeps the ones existing data needs)")
	cmd.Flags().String("server-ip", "", "server LAN IP")
	cmd.Flags().String("tz", "", "timezone")
	cmd.Flags().String("pihole-dns-bind", "", "Pi-hole :53 bind IP")
	cmd.Flags().Bool("vw-signups", true, "allow open Vaultwarden signups")
	cmd.Flags().String("disable-apps", "", "comma-separated apps to switch off (e.g. stirling,it-tools)")
	return cmd
}

// runSetup hands setup the flags that were actually given.
func (a *app) runSetup(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	var o setup.Options
	o.Yes, _ = f.GetBool("yes")
	o.Force, _ = f.GetBool("force")
	str := func(name string) *string {
		if !f.Changed(name) {
			return nil
		}
		v, _ := f.GetString(name)
		return &v
	}
	o.ServerIP, o.TZ, o.PiholeDNSBind = str("server-ip"), str("tz"), str("pihole-dns-bind")
	if f.Changed("vw-signups") {
		v, _ := f.GetBool("vw-signups")
		o.VWSignups = &v
	}
	if v := str("disable-apps"); v != nil {
		apps := config.SplitCSV(*v)
		o.DisabledApps = &apps
	}
	return a.setup.Run(o)
}

// setupArgs rejects stray arguments — above all a bool flag's value given after a space
// (`--vw-signups false`), which Cobra would otherwise take as an argument while the flag stays true.
func setupArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if v := strings.ToLower(args[0]); v == "true" || v == "false" {
		return fmt.Errorf("unexpected %q — give a true/false flag its value with '=', e.g. --vw-signups=%s", args[0], v)
	}
	return fmt.Errorf("unexpected argument %q — setup takes only flags (see hsctl setup --help)", args[0])
}

// appsCmd is `hsctl apps [enable|disable NAME]`.
func (a *app) appsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "apps", Short: "List apps and switch them on/off (data is kept)",
		Args: cobra.NoArgs, RunE: a.inRepo(func(*cobra.Command, []string) error {
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "APP\tSTATE\tRUNNING\tNAME")
			for _, app := range a.stack.Apps(true) {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", app.Dir, config.BoolStr(app.Enabled, "enabled", "disabled"),
					config.BoolStr(app.Running, "yes", "no"), app.Name)
			}
			return tw.Flush()
		})}
	for _, v := range []struct {
		use, short string
		on         bool
	}{
		{"enable NAME", "Switch an app on and start it", true},
		{"disable NAME", "Switch an app off and stop it (data volumes are kept)", false},
	} {
		on := v.on
		cmd.AddCommand(&cobra.Command{Use: v.use, Short: v.short, Args: cobra.ExactArgs(1),
			RunE: a.inRepo(a.locked("apps "+strings.Fields(v.use)[0], 0, func(_ *cobra.Command, args []string) error {
				return a.stack.SetEnabled(args[0], on, os.Stdout)
			}))})
	}
	return cmd
}
