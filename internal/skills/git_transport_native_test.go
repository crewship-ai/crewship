//go:build !windows

package skills

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These POSIX acceptance fixtures require Git, git-http-backend and an owned
// private IPv4 interface. Missing prerequisites fail rather than hiding coverage.
// Native acceptance deliberately constructs an INTERNAL approved plan. The
// production planner never permits loopback, including for allowlisted hosts.
// No public bypass, external DNS, live service, or TLS verification disable.
func TestPinnedGitNativeTLS(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("native acceptance requires Git on PATH: %v", err)
	}
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := qualifyPinnedGit(ctx, gitPath, home); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := runIsolatedGit(ctx, gitPath, home, args...)
		if err != nil {
			t.Fatalf("owned git setup: %v: %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	execPath := git("--exec-path")
	backend := filepath.Join(execPath, "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Fatalf("native acceptance requires git-http-backend at %s: %v", backend, err)
	}
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "source")
	git("init", src)
	content := []byte("---\nname: fixture\ndescription: owned fixture\nlicense: MIT\n---\nFixture body.\n")
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), content, 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", src, "add", "SKILL.md")
	git("-C", src, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	git("clone", "--bare", "--", src, filepath.Join(root, "repo.git"))
	git("-C", filepath.Join(root, "repo.git"), "config", "uploadpack.allowFilter", "true")
	cert, caPath := ownedGitCertificate(t)
	var contacts atomic.Int32
	var badAuthority atomic.Bool
	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacts.Add(1)
		if r.TLS == nil || r.TLS.ServerName != "localhost" || !strings.HasPrefix(r.Host, "localhost:") {
			badAuthority.Store(true)
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "request", 500)
			return
		}
		cmd := exec.CommandContext(r.Context(), backend)
		cmd.Dir = home
		cmd.Env = append(isolatedGitEnv(gitPath, home), "GIT_PROJECT_ROOT="+root, "GIT_HTTP_EXPORT_ALL=1",
			"GATEWAY_INTERFACE=CGI/1.1", "REQUEST_METHOD="+r.Method, "PATH_INFO="+r.URL.Path, "QUERY_STRING="+r.URL.RawQuery,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"), "CONTENT_LENGTH="+strconv.Itoa(len(body)),
			"SERVER_PROTOCOL=HTTP/1.1", "SERVER_NAME=localhost", "REMOTE_ADDR=127.0.0.1", "GIT_PROTOCOL="+r.Header.Get("Git-Protocol"))
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, "owned backend failed", 500)
			return
		}
		reader := bufio.NewReader(bytes.NewReader(out))
		headers, err := textproto.NewReader(reader).ReadMIMEHeader()
		if err != nil {
			http.Error(w, "CGI headers", 500)
			return
		}
		status := 200
		if value := headers.Get("Status"); value != "" {
			if n, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
				status = n
			}
		}
		headers.Del("Status")
		for k, values := range headers {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = io.Copy(w, reader)
	})
	server := ownedGitTLSServer(t, cert, "127.0.0.1", backendHandler)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	plan := gitClonePlan{url: "https://localhost:" + port + "/repo.git", host: "localhost", port: port, address: net.ParseIP("127.0.0.1"), caFile: caPath}
	template := t.TempDir()
	runClone := func(p gitClonePlan, wrongPin bool) (string, error) {
		destination := filepath.Join(t.TempDir(), "checkout")
		args, err := pinnedGitCloneArgs(p, template, destination, "")
		if err != nil {
			return destination, err
		}
		if wrongPin {
			wrongIP, seen, closeWrong, err := gitProbeWrongEndpoint(p.port)
			if err != nil {
				return destination, err
			}
			defer closeWrong()
			p.address = wrongIP
			args, err = pinnedGitCloneArgs(p, template, destination, "")
			if err != nil {
				return destination, err
			}
			defer func() {
				select {
				case <-seen:
				default:
					t.Error("wrong-pin control never reached its owned endpoint")
				}
			}()
		}

		bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		_, err = runIsolatedGit(bounded, gitPath, home, args...)
		return destination, err
	}
	// Synthetic inherited overrides must not change TLS, proxy or redirects.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.sslVerify")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_SSL_NO_VERIFY", "true")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'http.followRedirects'='true'")
	checkout, err := runClone(plan, false)
	if err != nil {
		t.Fatalf("pinned verified TLS clone: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(checkout, "SKILL.md"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("checkout differs: %v", err)
	}
	if contacts.Load() == 0 || badAuthority.Load() {
		t.Fatal("missing native HTTPS contact or wrong Host/SNI")
	}
	before := contacts.Load()
	if _, err := runClone(plan, true); err == nil {
		t.Fatal("wrong pin unexpectedly cloned the positive listener")
	}
	if contacts.Load() != before {
		t.Fatal("wrong pin contacted the positive fixture")
	}
	noCA := plan
	noCA.caFile = ""
	if _, err := runClone(noCA, false); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}
	var redirected atomic.Int32
	target := ownedGitTLSServer(t, cert, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); http.Error(w, "unexpected", 500) }))
	targetURL := "https://localhost:" + strings.Split(target.Listener.Addr().String(), ":")[1] + "/repo.git"
	var redirectOrigin atomic.Int32
	redirect := ownedGitTLSServer(t, cert, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectOrigin.Add(1)
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	_, redirectPort, _ := net.SplitHostPort(redirect.Listener.Addr().String())
	redirectPlan := plan
	redirectPlan.port = redirectPort
	redirectPlan.url = "https://localhost:" + redirectPort + "/repo.git"
	if _, err := runClone(redirectPlan, false); err == nil {
		t.Fatal("redirect unexpectedly accepted")
	}
	if redirectOrigin.Load() == 0 || redirected.Load() != 0 {
		t.Fatal("redirect origin not contacted or target followed")
	}

	t.Run("complete-import-with-admin-private-host", func(t *testing.T) {
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			t.Fatal(err)
		}
		var ownedIP net.IP
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr == nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsLoopback() {
				ownedIP = ip
				break
			}
		}
		if ownedIP == nil {
			t.Fatal("native acceptance requires an owned private IPv4 interface for the administrator-allowlisted import fixture")
		}
		privateCert, privateCA := ownedGitCertificate(t, ownedIP)
		private := ownedGitTLSServer(t, privateCert, ownedIP.String(), backendHandler)
		// Simulate an administrator-installed Git trust store in an owned
		// executable wrapper. Qualification passes its own CA after this
		// argument. No production planner, TLS or policy bypass is added.
		wrapperDir := t.TempDir()
		wrapper := filepath.Join(wrapperDir, "git")
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
		body := "#!/bin/sh\nexec " + quote(gitPath) + " -c " + quote("http.sslCAInfo="+privateCA) + " \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		imp := newCovImporter(t)
		privateURL := "https://" + private.Listener.Addr().String() + "/repo.git"
		if _, err := imp.BulkImport(ctx, BulkImportRequest{GitURL: privateURL}); err == nil {
			t.Fatal("private destination accepted without administrator allowlist")
		}
		imp.gitPolicy = GitImportPolicy{PrivateHosts: []string{ownedIP.String()}}
		result, err := imp.BulkImport(ctx, BulkImportRequest{GitURL: privateURL})
		if err != nil || result.TotalImported != 1 {
			t.Fatalf("complete import: result=%+v err=%v", result, err)
		}
		var count int
		if err := imp.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM skills WHERE name = ?", "fixture").Scan(&count); err != nil || count != 1 {
			t.Fatalf("native import did not persist owned skill: count=%d err=%v", count, err)
		}
	})

	// Force the dumb transport far enough to request alternates. Removing
	// shallow mode is a deliberate stronger test of followRedirects=false;
	// the exact production flags also refuse dumb shallow repositories.
	var alternateRequests atomic.Int32
	var alternateTargets atomic.Int32
	var alternateSockets atomic.Int32
	foreign := ownedGitTLSServer(t, cert, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		alternateTargets.Add(1)
		http.NotFound(w, r)
	}), &alternateSockets)
	_, foreignPort, _ := net.SplitHostPort(foreign.Listener.Addr().String())
	oid := git("-C", src, "rev-parse", "HEAD")
	dumb := ownedGitTLSServer(t, cert, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dumb.git/info/refs":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, oid+"\trefs/heads/main\n")
		case "/dumb.git/HEAD":
			_, _ = io.WriteString(w, "ref: refs/heads/main\n")
		case "/dumb.git/objects/info/http-alternates":
			alternateRequests.Add(1)
			_, _ = io.WriteString(w, "https://localhost:"+foreignPort+"/foreign.git/objects\n")
		default:
			http.NotFound(w, r)
		}
	}))
	_, dumbPort, _ := net.SplitHostPort(dumb.Listener.Addr().String())
	dumbPlan := plan
	dumbPlan.port, dumbPlan.url = dumbPort, "https://localhost:"+dumbPort+"/dumb.git"
	runDumb := func(allowAlternates bool) {
		t.Helper()
		args, err := pinnedGitCloneArgs(dumbPlan, template, filepath.Join(t.TempDir(), "checkout"), "")
		if err != nil {
			t.Fatal(err)
		}
		for i, a := range args {
			if a == "--depth" {
				args = append(args[:i], args[i+2:]...)
				break
			}
		}
		if allowAlternates {
			for i, a := range args {
				if a == "clone" {
					args = append(append(append([]string{}, args[:i]...), "-c", "http.followRedirects=true"), args[i:]...)
					break
				}
			}
		}
		bounded, stop := context.WithTimeout(ctx, 8*time.Second)
		defer stop()
		_, _ = runIsolatedGit(bounded, gitPath, home, args...)
	}
	runDumb(false)
	if alternateRequests.Load() == 0 || alternateTargets.Load() != 0 || alternateSockets.Load() != 0 {
		t.Fatalf("alternates deny control: origin=%d foreign=%d", alternateRequests.Load(), alternateTargets.Load())
	}
	runDumb(true)
	if alternateTargets.Load() == 0 || alternateSockets.Load() == 0 {
		t.Fatal("alternate contact positive control was not reached")
	}

}

func ownedGitTLSServer(t *testing.T, certificate tls.Certificate, ip string, handler http.Handler, accepts ...*atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(accepts) > 0 {
		listener = &countingGitListener{Listener: listener, count: accepts[0]}
	}
	server.Listener = listener
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func ownedGitCertificate(t *testing.T, ips ...net.IP) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Owned Git fixture"}, DNSNames: []string{"localhost"}, IPAddresses: ips, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "owned-ca.pem")
	if err := os.WriteFile(path, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certificate, path
}

func TestGitQualificationFailsClosedForUnqualifiedHelper(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	body := "#!/bin/sh\ncase \"$*\" in\n *--version*) echo 'git version 2.43.0';;\n *help*) echo 'http.curloptResolve';;\n *) exit 0;;\nesac\n"
	if err := os.WriteFile(fake, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := qualifyPinnedGit(ctx, fake, t.TempDir()); err == nil {
		t.Fatal("version/config strings and a zero exit code cannot prove TLS pinning")
	}
}

type countingGitListener struct {
	net.Listener
	count *atomic.Int32
}

func (l *countingGitListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err == nil {
		l.count.Add(1)
	}
	return connection, err
}
