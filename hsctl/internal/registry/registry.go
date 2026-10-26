// Package registry talks to container registries' v2 HTTP API anonymously: it lists a
// repository's tags and fetches manifests, following the bearer-token challenge Docker Hub and
// ghcr.io ask for. No Docker, no pulls.
package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Client is a registry client.
type Client struct {
	HTTP *http.Client
}

// New returns a client with a 20s timeout per request.
func New() *Client { return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}} }

// SplitRef breaks an image reference into registry host, repository and tag
// ("vaultwarden/server:1.37.4" -> docker.io, vaultwarden/server, 1.37.4;
// "nginx:1.31.8-alpine" -> docker.io, library/nginx, 1.31.8-alpine).
func SplitRef(image string) (host, repo, tag string) {
	if ref, _, ok := strings.Cut(image, "@"); ok {
		image = ref
	}
	host, rest := "docker.io", image
	if first, _, ok := strings.Cut(image, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, rest = first, image[len(first)+1:]
	}
	repo, tag = rest, "latest"
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		repo, tag = rest[:i], rest[i+1:]
	}
	if host == "docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return host, repo, tag
}

// Tag returns just the tag of an image reference.
func Tag(image string) string {
	_, _, tag := SplitRef(image)
	return tag
}

// Retag swaps an image reference's tag: Retag("vaultwarden/server:1.37.3", "1.37.4") ->
// "vaultwarden/server:1.37.4". Any digest suffix is dropped.
func Retag(image, newTag string) string {
	if ref, _, ok := strings.Cut(image, "@"); ok {
		image = ref
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image + ":" + newTag
}

// Base maps a registry host to its v2 API base URL. Docker Hub's API lives on
// registry-1.docker.io, not docker.io.
func Base(host string) string {
	if host == "docker.io" {
		return "https://registry-1.docker.io"
	}
	return "https://" + host
}

var authFieldRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// bearerToken performs the anonymous token dance a v2 registry asks for via
// WWW-Authenticate: Bearer realm="...",service="...",scope="...".
func (c *Client) bearerToken(wwwAuth string) string {
	fields := map[string]string{}
	for _, kv := range authFieldRE.FindAllStringSubmatch(wwwAuth, -1) {
		fields[kv[1]] = kv[2]
	}
	realm := fields["realm"]
	if !strings.HasPrefix(wwwAuth, "Bearer ") || realm == "" {
		return ""
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope"} {
		if fields[k] != "" {
			q.Set(k, fields[k])
		}
	}
	resp, err := c.HTTP.Get(realm + "?" + q.Encode())
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return ""
	}
	return body.Token
}

// Get performs a v2 registry GET, following the anonymous bearer-token challenge. accept selects
// the manifest media types (empty for none).
func (c *Client) Get(u, accept string) (*http.Response, error) {
	get := func(token string) (*http.Response, error) {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return c.HTTP.Do(req)
	}
	resp, err := get("")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	auth := resp.Header.Get("Www-Authenticate")
	resp.Body.Close()
	token := c.bearerToken(auth)
	if token == "" {
		return nil, fmt.Errorf("registry auth failed")
	}
	return get(token)
}

var nextLinkRE = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// Tags lists a repository's tags (base is e.g. https://registry-1.docker.io), following
// Link-header pagination.
func (c *Client) Tags(base, repo string) ([]string, error) {
	next := base + "/v2/" + repo + "/tags/list?n=1000"
	var tags []string
	for page := 0; next != "" && page < 50; page++ {
		resp, err := c.Get(next, "")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("registry returned %s for %s", resp.Status, repo)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		tags = append(tags, body.Tags...)
		next = ""
		if m := nextLinkRE.FindStringSubmatch(link); m != nil {
			next = m[1]
			if strings.HasPrefix(next, "/") {
				next = base + next
			}
		}
	}
	return tags, nil
}
