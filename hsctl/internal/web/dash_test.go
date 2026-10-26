//go:build integration

package web_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/devices"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/jobs"
	"github.com/Rishikesh01/homeserver/hsctl/internal/oplock"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
	"github.com/Rishikesh01/homeserver/hsctl/internal/sysinfo"
	"github.com/Rishikesh01/homeserver/hsctl/internal/updates"
	"github.com/Rishikesh01/homeserver/hsctl/internal/web"
)

const password = "itest-dashboard-pw"

// syncBuffer collects log lines written from the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// dash is the dashboard over real HTTP, wired to the real services for a checkout, with a browser
// that keeps cookies and doesn't follow redirects.
type dash struct {
	t    *testing.T
	repo paths.Repo
	url  string
	c    *http.Client
	s    *web.Server
	log  *syncBuffer
	lock *oplock.Locker
}

func newDash(t *testing.T, repo paths.Repo, reg *registry.Client) *dash {
	t.Helper()
	out := io.Discard
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = out, out
	store := config.NewStore(repo, config.LocalHost{})
	prov := provision.New(repo, dk)
	prov.Out, prov.Err = out, out
	st := stack.New(repo, dk, store, prov)
	st.Out, st.Err = out, out
	rs := restic.New(repo.Path(paths.ResticPassword), backup.CredentialsEnv(repo))
	rs.Stdout, rs.Stderr = out, out
	bk := backup.New(repo, dk, rs, st, &prompt.Terminal{Interactive: func() bool { return false }})
	bk.Out, bk.Err = out, out
	if reg == nil {
		reg = registry.New()
	}
	upd := updates.New(repo, dk, reg, st.ContainerNames, nil)
	upd.Out = out
	lock := oplock.New(repo.Root)
	lock.Notice = out
	devs := devices.New(priv.Root{})
	// Jobs run the real hsctl (tests that start one call itest.Hsctl first).
	jr := jobs.New(repo.Root, os.Getenv("HSCTL_BIN"), nil)
	jr.Dir = t.TempDir()
	s := web.New(repo, password, web.Deps{
		Settings: store, Provision: prov, Stack: st, Backup: bk, Restic: rs, Updates: upd, Devices: devs,
		Sysinfo: sysinfo.New(devs, priv.Root{}, func() (string, bool) { return "", false }),
		Docker:  dk, Jobs: jr, Lock: lock, Priv: priv.Root{},
	})
	d := &dash{t: t, repo: repo, s: s, log: &syncBuffer{}, lock: lock}
	s.Log = slog.New(slog.NewTextHandler(d.log, nil))
	s.Out = io.Discard
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	d.url = srv.URL
	d.c = &http.Client{Jar: jar, Timeout: 3 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return d
}

// configured is a checkout set up for 10.0.0.5 with the heavy apps switched off.
func configured(t *testing.T) paths.Repo {
	t.Helper()
	repo := itest.Checkout(t)
	store := config.NewStore(repo, config.LocalHost{})
	c := store.Loaded()
	c.ServerIP = "10.0.0.5"
	c.DisabledApps = []string{"nextcloud", "pihole", "stirling"}
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := provision.New(repo, docker.New(repo.Root)).Generate(c, false); err != nil {
		t.Fatal(err)
	}
	return repo
}

// do sends a request; form, when set, is POSTed as a form. hdr adds headers ("Host" sets the host).
func (d *dash) do(method, path string, form url.Values, hdr map[string]string) (*http.Response, string) {
	d.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, d.url+path, body)
	if err != nil {
		d.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		if k == "Host" {
			// The client would pick cookies for this host; send the dashboard's, as a browser
			// reaching it under that name would.
			req.Host = v
			u, _ := url.Parse(d.url)
			for _, c := range d.c.Jar.Cookies(u) {
				req.AddCookie(c)
			}
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := d.c.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (d *dash) get(path string) (*http.Response, string) { return d.do("GET", path, nil, nil) }

func (d *dash) post(path string, form url.Values) (*http.Response, string) {
	if form == nil {
		form = url.Values{}
	}
	return d.do("POST", path, form, nil)
}

func (d *dash) login() {
	d.t.Helper()
	resp, body := d.post("/login", url.Values{"username": {"admin"}, "password": {password}})
	if resp.StatusCode != http.StatusSeeOther {
		d.t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
}

// startJob POSTs to a job-starting endpoint and returns the job's ID.
func (d *dash) startJob(path string, form url.Values) string {
	d.t.Helper()
	resp, body := d.post(path, form)
	var j struct{ ID string }
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &j) != nil || j.ID == "" {
		d.t.Fatalf("POST %s: %d %s", path, resp.StatusCode, body)
	}
	return j.ID
}

// waitJob follows a job the way the page does until it's done, returning its whole output.
func (d *dash) waitJob(id string) jobs.Status {
	d.t.Helper()
	var all strings.Builder
	var from int64
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		resp, body := d.get(fmt.Sprintf("/admin/job?id=%s&from=%d", id, from))
		var st jobs.Status
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &st) != nil {
			d.t.Fatalf("job %s: %d %s", id, resp.StatusCode, body)
		}
		all.WriteString(st.Out)
		from = st.Next
		if st.Done {
			st.Out = all.String()
			return st
		}
		time.Sleep(200 * time.Millisecond)
	}
	d.t.Fatalf("job %s didn't finish:\n%s", id, all.String())
	return jobs.Status{}
}
