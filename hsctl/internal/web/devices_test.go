//go:build integration

package web_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// TestDrives mounts and ejects a real disk image from the Drives page, and refuses to while another
// operation (a backup, say) may be using the disks.
func TestDrives(t *testing.T) {
	itest.NeedRoot(t)
	label := strings.TrimPrefix(itest.Name("web"), "hsctl-it-")
	dev := itest.LoopDisk(t, label)

	d := newDash(t, itest.Repo(t), nil)
	d.login()
	act := func(path string, form url.Values) string {
		resp, body := d.post(path, form)
		if resp.StatusCode != 303 || resp.Header.Get("Location") != "/admin/devices" {
			t.Fatalf("POST %s = %d %s", path, resp.StatusCode, body)
		}
		_, body = d.get("/admin/devices")
		return body
	}
	steps := []struct {
		name string
		do   func() string
		want []string
	}{
		{"listed, with where it would go", func() string { _, body := d.get("/admin/devices"); return body },
			[]string{dev, label, "/mnt/" + label}},
		{"busy", func() string {
			l, err := d.lock.Acquire("backup run", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Release()
			return act("/admin/devices/mount", url.Values{"dev": {dev}})
		}, []string{"Not now — backup run"}},
		{"mount", func() string { return act("/admin/devices/mount", url.Values{"dev": {dev}}) }, []string{"Mounted at /mnt/" + label}},
		{"eject", func() string { return act("/admin/devices/unmount", url.Values{"dev": {dev}}) },
			[]string{"Ejected (unmounted /mnt/" + label + ") — safe to unplug."}},
		{"eject what isn't mounted", func() string { return act("/admin/devices/unmount", url.Values{"dev": {dev}}) },
			[]string{"Eject failed: " + dev + " is not mounted"}},
	}
	for _, st := range steps {
		body := st.do()
		for _, w := range st.want {
			if !strings.Contains(body, w) {
				t.Fatalf("%s: lacks %q\n%s", st.name, w, body)
			}
		}
	}
}
