//go:build integration

package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/web"
)

// TestLogin: the admin pages need a session; the login form sets one — HttpOnly, SameSite, Secure
// behind Caddy's TLS — and sends you on only to a page of this site; logout ends it server-side.
func TestLogin(t *testing.T) {
	cases := []struct {
		name, user, pass, next string
		https                  bool
		wantCode               int
		wantLocation           string
	}{
		{"wrong password", "admin", "nope", "/admin", false, 401, ""},
		{"wrong user", "root", password, "/admin", false, 401, ""},
		{"to the page asked for", "admin", password, "/admin/terminal", false, 303, "/admin/terminal"},
		{"default", "admin", password, "", false, 303, "/admin"},
		{"behind Caddy's TLS", "admin", password, "/admin", true, 303, "/admin"},
		{"protocol-relative", "admin", password, "//evil.com", false, 303, "/admin"},
		{"backslash folded by browsers", "admin", password, `/\evil.com`, false, 303, "/admin"},
		{"absolute URL", "admin", password, "https://evil.com", false, 303, "/admin"},
		{"tab stripped by browsers", "admin", password, "/\t/evil.com", false, 303, "/admin"},
		{"newline", "admin", password, "/\n/evil.com", false, 303, "/admin"},
		{"space", "admin", password, "/admin backup", false, 303, "/admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDash(t, itest.Repo(t), nil)
			hdr := map[string]string{}
			if tc.https {
				hdr["X-Forwarded-Proto"] = "https"
			}
			resp, body := d.do("POST", "/login", url.Values{"username": {tc.user}, "password": {tc.pass}, "next": {tc.next}}, hdr)
			if resp.StatusCode != tc.wantCode || resp.Header.Get("Location") != tc.wantLocation {
				t.Fatalf("login = %d %q\n%s", resp.StatusCode, resp.Header.Get("Location"), body)
			}
			if tc.wantCode != 303 {
				if !strings.Contains(body, "Wrong username or password") {
					t.Errorf("no explanation:\n%s", body)
				}
				return
			}
			var c *http.Cookie
			for _, ck := range resp.Cookies() {
				if ck.Name == "hsctl_session" {
					c = ck
				}
			}
			if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure != tc.https {
				t.Errorf("session cookie %+v", c)
			}
		})
	}
}

// TestSession: without a session the admin pages send you to log in (the polled JSON endpoints
// say so instead), a made-up cookie is no session, and logging out ends it on the server.
func TestSession(t *testing.T) {
	d := newDash(t, itest.Repo(t), nil)
	steps := []struct {
		name         string
		do           func() (*http.Response, string)
		wantCode     int
		wantLocation string
	}{
		{"admin page", func() (*http.Response, string) { return d.get("/admin/backup?x=1") }, 303, "/login?next=%2Fadmin%2Fbackup%3Fx%3D1"},
		{"polled job status", func() (*http.Response, string) { return d.get("/admin/job?id=x") }, 401, ""},
		{"forged cookie", func() (*http.Response, string) {
			return d.do("GET", "/admin", nil, map[string]string{"Cookie": "hsctl_session=forged"})
		}, 303, "/login?next=%2Fadmin"},
		{"login page", func() (*http.Response, string) { return d.get("/login?next=/admin/apps") }, 200, ""},
		{"logged in", func() (*http.Response, string) { d.login(); return d.get("/admin") }, 200, ""},
		{"logout", func() (*http.Response, string) { return d.get("/logout") }, 303, "/login"},
	}
	var cookie string
	for _, st := range steps {
		if st.name == "logout" {
			u, _ := url.Parse(d.url)
			for _, c := range d.c.Jar.Cookies(u) {
				if c.Name == "hsctl_session" {
					cookie = c.Value
				}
			}
		}
		resp, body := st.do()
		if resp.StatusCode != st.wantCode || resp.Header.Get("Location") != st.wantLocation {
			t.Fatalf("%s = %d %q\n%s", st.name, resp.StatusCode, resp.Header.Get("Location"), body)
		}
	}
	// The old cookie, replayed after logout, is dead on the server too.
	resp, _ := d.do("GET", "/admin", nil, map[string]string{"Cookie": "hsctl_session=" + cookie})
	if cookie == "" || resp.StatusCode != 303 {
		t.Errorf("a logged-out session still works (%q): %d", cookie, resp.StatusCode)
	}
}

// TestThrottle: a few wrong passwords are free, then the client is locked out — even with the right
// password — and a forged X-Forwarded-For can't escape the per-connection budget.
func TestThrottle(t *testing.T) {
	try := func(d *dash, client, pw string) int {
		hdr := map[string]string{}
		if client != "" {
			hdr["X-Forwarded-For"] = client
		}
		resp, body := d.do("POST", "/login", url.Values{"username": {"admin"}, "password": {pw}}, hdr)
		if resp.StatusCode == 429 && !strings.Contains(body, "Too many wrong passwords — wait") {
			t.Errorf("a lockout must say how long: %s", body)
		}
		return resp.StatusCode
	}
	cases := []struct {
		name  string
		steps func(d *dash) []int
		want  []int
	}{
		{"free tries, then locked out", func(d *dash) []int {
			var got []int
			for i := 0; i < 7; i++ {
				got = append(got, try(d, "", "wrong"))
			}
			return append(got, try(d, "", password))
		}, []int{401, 401, 401, 401, 401, 401, 429, 429}},
		{"another client isn't locked out by one", func(d *dash) []int {
			for i := 0; i < 6; i++ {
				try(d, "192.168.1.50", "wrong")
			}
			return []int{try(d, "192.168.1.50", password), try(d, "192.168.1.51", password)}
		}, []int{429, 303}},
		{"rotating forwarded addresses hit the connection's budget", func(d *dash) []int {
			for i := 0; i < 31; i++ {
				try(d, fmt.Sprintf("10.0.0.%d", i), "wrong")
			}
			return []int{try(d, "10.9.9.9", password)}
		}, []int{429}},
		{"success resets the count", func(d *dash) []int {
			for i := 0; i < 4; i++ {
				try(d, "", "wrong")
			}
			ok := try(d, "", password)
			var got []int
			for i := 0; i < 5; i++ {
				got = append(got, try(d, "", "wrong"))
			}
			return append([]int{ok}, got...)
		}, []int{303, 401, 401, 401, 401, 401}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.steps(newDash(t, itest.Repo(t), nil))
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("codes %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoginBurst: parallel wrong passwords must hit the lockout too — requests queued behind a
// failure don't get their guess compared anyway.
func TestLoginBurst(t *testing.T) {
	cases := []struct {
		parallel   int
		maxChecked int
	}{{20, 6}}
	for _, tc := range cases {
		d := newDash(t, itest.Repo(t), nil)
		codes := make(chan int, tc.parallel)
		var wg sync.WaitGroup
		for i := 0; i < tc.parallel; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, err := d.c.PostForm(d.url+"/login", url.Values{"username": {"admin"}, "password": {"wrong"}})
				if err == nil {
					resp.Body.Close()
					codes <- resp.StatusCode
				}
			}()
		}
		wg.Wait()
		close(codes)
		count := map[int]int{}
		for c := range codes {
			count[c]++
		}
		if count[401] > tc.maxChecked || count[429] == 0 {
			t.Errorf("responses %v: want at most %d password checks, the rest throttled", count, tc.maxChecked)
		}
	}
}

// TestSameOrigin: an admin POST from another origin — another app on this box, on another port —
// is refused; the dashboard's own pages, and clients that send no browser headers, get through.
func TestSameOrigin(t *testing.T) {
	d := newDash(t, configured(t), nil)
	d.login()
	host := strings.TrimPrefix(d.url, "http://")
	cases := []struct {
		name string
		hdr  map[string]string
		want bool
	}{
		{"own page, direct", map[string]string{"Origin": d.url}, true},
		{"own page over Caddy", map[string]string{"Origin": "https://10.0.0.5", "X-Forwarded-Proto": "https", "Host": "10.0.0.5"}, true},
		{"the server's address from Caddy's front", map[string]string{"Origin": "https://10.0.0.5"}, true},
		{"sibling app port", map[string]string{"Origin": "https://10.0.0.5:8444"}, false},
		{"other site", map[string]string{"Origin": "https://evil.example"}, false},
		{"referer only, sibling", map[string]string{"Referer": "https://10.0.0.5:8447/x"}, false},
		{"referer only, own", map[string]string{"Referer": "http://" + host + "/admin"}, true},
		{"opaque origin", map[string]string{"Origin": "null"}, false},
		{"no browser headers (curl)", nil, true},
	}
	for _, tc := range cases {
		resp, body := d.do("POST", "/admin/run", url.Values{"slug": {"no-such-command"}}, tc.hdr)
		refused := resp.StatusCode == 403 && strings.Contains(body, "cross-site request refused")
		if refused == tc.want || (!refused && resp.StatusCode != 404) {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
}

// TestRequestLog: every request is logged with its real status; the polled and static paths only
// at debug level, so they don't bury the journal.
func TestRequestLog(t *testing.T) {
	d := newDash(t, itest.Repo(t), nil)
	d.login()
	cases := []struct {
		path   string
		want   string // "" = must not be logged at info level
		header map[string]string
	}{
		{"/nope", "path=/nope status=404", nil},
		{"/admin", "path=/admin status=200", nil},
		{"/login", "from=192.168.1.50", map[string]string{"X-Forwarded-For": "192.168.1.50, 172.18.0.2"}},
		{"/admin/assets/xterm.js", "", nil},
		{"/admin/job?id=x", "", nil},
	}
	for _, tc := range cases {
		d.do("GET", tc.path, nil, tc.header)
		path, _, _ := strings.Cut(tc.path, "?")
		if tc.want == "" && strings.Contains(d.log.String(), "path="+path+" ") {
			t.Errorf("%s logged at info level:\n%s", tc.path, d.log.String())
		}
		if tc.want != "" && !strings.Contains(d.log.String(), tc.want) {
			t.Errorf("log lacks %q:\n%s", tc.want, d.log.String())
		}
	}
}

// TestListen: the dashboard listens where Caddy forwards to (caddy/.env), on loopback and the docker
// bridge — never the LAN, unless the bridge can't be found (then everywhere, with a warning).
func TestListen(t *testing.T) {
	addrs := []struct {
		port     int
		gateway  string
		want     string
		wantWarn bool
	}{
		{8088, "172.17.0.1", "127.0.0.1:8088 172.17.0.1:8088", false},
		{9000, "", "0.0.0.0:9000", true},
	}
	for _, tc := range addrs {
		got, warn := web.ListenAddrs(tc.port, tc.gateway)
		if strings.Join(got, " ") != tc.want || (warn != "") != tc.wantWarn || (warn != "" && !strings.Contains(warn, "--addr 127.0.0.1:9000")) {
			t.Errorf("ListenAddrs(%d, %q) = %v, %q", tc.port, tc.gateway, got, warn)
		}
	}
	ports := []struct {
		caddyEnv string
		want     int
	}{
		{"", 9000},
		{"HOME_UPSTREAM=host.docker.internal:8099\n", 8099},
		{"HOME_UPSTREAM=dashboard:8099\n", 9000},
	}
	for _, tc := range ports {
		repo := itest.Repo(t)
		if tc.caddyEnv != "" {
			itest.WriteFile(t, repo.Root, "caddy/.env", tc.caddyEnv)
		}
		if got := web.DashboardPort(repo, config.Settings{UIPort: 9000}); got != tc.want {
			t.Errorf("DashboardPort(%q) = %d, want %d", tc.caddyEnv, got, tc.want)
		}
	}
}
