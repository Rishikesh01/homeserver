package stack

import (
	"fmt"
	"io"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/catalog"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// Apps can be switched on and off — at first setup (the wizard's checkboxes) and later (the Apps
// admin page, or `hsctl apps enable|disable`). "Off" means: `hsctl up` skips the app's compose
// project, its containers are stopped, and its tile disappears from the home page. Its data volumes
// and .env are KEPT, so switching it back on is lossless. Caddy is never an app here — it's the
// front door for whatever is on.
//
// The switch is a list of compose folders in setup.conf (DISABLED_APPS=stirling,it-tools), so it
// survives re-setup and rides along in backups like every other setting.

// App is one switchable app, with its tile's name and blurb (or its folder name when it has no tile).
type App struct {
	Dir, Name, Icon, Desc string
	Enabled               bool
	Running               bool // any container of the compose project is up (best-effort)
}

// Apps lists the switchable apps in start order; withState also asks Docker which are running.
func (s *Service) Apps(withState bool) []App {
	c := s.Settings.Loaded()
	tiles := map[string]catalog.Tile{}
	for _, t := range catalog.Load(s.Repo.Path(paths.Services)) {
		if t.Dir != "" {
			tiles[t.Dir] = t
		}
	}
	var out []App
	for _, d := range layout.Apps() {
		a := App{Dir: d, Name: d, Icon: "📦", Enabled: !c.IsDisabled(d)}
		if t, ok := tiles[d]; ok {
			a.Name, a.Icon, a.Desc = t.Name, t.Icon, t.Desc
		}
		if withState {
			out2, err := s.Docker.Output(s.Repo.Path(d), "compose", "ps", "-q", "--status", "running")
			a.Running = err == nil && strings.TrimSpace(out2) != ""
		}
		out = append(out, a)
	}
	return out
}

// SetEnabled flips an app's switch, saves setup.conf, and applies it to the running stack: enable =
// compose up -d (after the edge network exists), disable = compose down (volumes kept). Progress
// goes to w. The settings are saved first so a docker hiccup never leaves the saved state and the
// intended state disagreeing for the next `hsctl up`.
func (s *Service) SetEnabled(dir string, enabled bool, w io.Writer) error {
	if !layout.IsApp(dir) {
		return fmt.Errorf("unknown app %q — one of: %s", dir, strings.Join(layout.Apps(), ", "))
	}
	c := s.Settings.Loaded()
	c.SetDisabled(dir, !enabled)
	if err := s.Settings.Save(c); err != nil {
		return err
	}
	if enabled {
		for _, m := range s.Provision.MissingEnv(c) {
			if m == dir {
				return fmt.Errorf("%s has no .env yet — run the setup wizard (or `hsctl setup`) first", dir)
			}
		}
		if err := s.Docker.EnsureNetwork(layout.EdgeNetwork); err != nil {
			return err
		}
		fmt.Fprintf(w, "== enabling %s: starting ==\n", dir)
		if out, err := s.Docker.Combined(s.Repo.Path(dir), s.Docker.ComposeUpArgs()...); err != nil {
			return fmt.Errorf("%s: %v\n%s", dir, err, strings.TrimSpace(out))
		}
		fmt.Fprintf(w, "%s is on.\n", dir)
		return nil
	}
	fmt.Fprintf(w, "== disabling %s: stopping (data kept) ==\n", dir)
	if out, err := s.Docker.Combined(s.Repo.Path(dir), "compose", "down"); err != nil {
		return fmt.Errorf("%s: %v\n%s", dir, err, strings.TrimSpace(out))
	}
	fmt.Fprintf(w, "%s is off. Its data is kept; enable it again any time.\n", dir)
	return nil
}
