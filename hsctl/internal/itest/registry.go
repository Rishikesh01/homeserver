package itest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RegistryImage is the registry the fixture runs.
const RegistryImage = "registry:2.8.3"

// RegistryName is the registry's hostname. Docker and buildx talk plain HTTP to any registry on
// 127.0.0.1, while hsctl always uses HTTPS — a name makes every client use TLS.
const RegistryName = "registry.hsctl.test"

// Registry is a throwaway container registry with TLS, run by the test's Docker daemon: a real
// registry for the update and platform checks to talk to, and for Docker to pull from.
type Registry struct {
	Host string       // registry.hsctl.test:<port>
	HTTP *http.Client // trusts the registry's certificate
}

// StartRegistry runs a registry for the rest of the test. RegistryName resolves to 127.0.0.1 (the
// harness puts it in /etc/hosts before the daemon starts), and the certificate is trusted by the Docker daemon (certs.d) and system-wide (docker buildx reads the
// system store) in the throwaway test container.
func StartRegistry(t *testing.T) *Registry {
	t.Helper()
	NeedRoot(t)
	NeedDocker(t)
	Pull(t, RegistryImage)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	certPEM, keyPEM := selfSigned(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"cert.pem": certPEM, "key.pem": keyPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	name := Name("registry")
	Docker(t, "run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:5000", port),
		"-v", dir+":/certs:ro", "-e", "REGISTRY_HTTP_TLS_CERTIFICATE=/certs/cert.pem",
		"-e", "REGISTRY_HTTP_TLS_KEY=/certs/key.pem", RegistryImage)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", name).Run() })
	host := fmt.Sprintf("%s:%d", RegistryName, port)
	if !strings.Contains(ReadFile("/etc/hosts"), " "+RegistryName+"\n") {
		t.Skip("needs " + RegistryName + " in /etc/hosts (make test-integration adds it)")
	}
	WriteFile(t, "/etc/docker/certs.d/"+host, "ca.crt", string(certPEM))
	WriteFile(t, "/usr/local/share/ca-certificates", name+".crt", string(certPEM))
	if out, err := exec.Command("update-ca-certificates").CombinedOutput(); err != nil {
		t.Fatalf("update-ca-certificates: %v\n%s", err, out)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	r := &Registry{Host: host,
		HTTP: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := r.HTTP.Get("https://" + r.Host + "/v2/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry didn't come up: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// selfSigned makes a certificate for RegistryName that is its own root.
func selfSigned(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "hsctl integration registry"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		DNSNames:              []string{RegistryName},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

// Push tags the local image src as <registry>/<repoTag>, pushes it, and returns that reference.
func (r *Registry) Push(t testing.TB, src, repoTag string) string {
	t.Helper()
	ref := r.Host + "/" + repoTag
	Docker(t, "tag", src, ref)
	Docker(t, "push", "-q", ref)
	return ref
}

// Index publishes repo:tag as a multi-platform index of the given platforms ("linux/amd64", …),
// each pointing at the manifest already pushed as repo:base. The registry only checks that the
// manifests exist, so one image can stand in for every platform.
func (r *Registry) Index(t testing.TB, repo, tag, base string, platforms ...string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://"+r.Host+"/v2/"+repo+"/manifests/"+base, nil)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest %s:%s: %s %s", repo, base, resp.Status, body)
	}
	type platform struct {
		OS   string `json:"os"`
		Arch string `json:"architecture"`
	}
	type entry struct {
		MediaType string   `json:"mediaType"`
		Digest    string   `json:"digest"`
		Size      int      `json:"size"`
		Platform  platform `json:"platform"`
	}
	idx := struct {
		SchemaVersion int     `json:"schemaVersion"`
		MediaType     string  `json:"mediaType"`
		Manifests     []entry `json:"manifests"`
	}{2, "application/vnd.docker.distribution.manifest.list.v2+json", nil}
	for _, p := range platforms {
		goos, arch, _ := strings.Cut(p, "/")
		idx.Manifests = append(idx.Manifests, entry{resp.Header.Get("Content-Type"), resp.Header.Get("Docker-Content-Digest"),
			len(body), platform{goos, arch}})
	}
	b, _ := json.Marshal(idx)
	put, _ := http.NewRequest("PUT", "https://"+r.Host+"/v2/"+repo+"/manifests/"+tag, bytes.NewReader(b))
	put.Header.Set("Content-Type", idx.MediaType)
	resp, err = r.HTTP.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT index %s:%s: %s %s", repo, tag, resp.Status, out)
	}
}
