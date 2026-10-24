package skills

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A version/config listing cannot prove that the actual HTTPS helper's
// runtime libcurl honored CURLOPT_RESOLVE: Git ignores its setopt return.
// Qualify each invocation with an owned TLS endpoint on a loopback address
// different from localhost's normal destination. No repository is served.
// This is internal qualification, never an exception to destination policy.
func qualifyPinnedGit(ctx context.Context, gitPath, home string) error {
	if err := requirePinnedGit(ctx, gitPath, home); err != nil {
		return err
	}
	certificate, publicCA, err := gitProbeCertificate()
	if err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	caPath := filepath.Join(home, "qualification-ca.pem")
	if err := os.WriteFile(caPath, publicCA, 0600); err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	defer func() { _ = os.Remove(caPath) }()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	path := "/" + hex.EncodeToString(nonce[:]) + ".git"
	listener, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("git import transport qualification unavailable")
	}
	seen := make(chan struct{}, 1)
	server := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.Host == "localhost:"+port && r.URL.Path == path+"/info/refs" && r.TLS != nil && r.TLS.ServerName == "localhost" {
				select {
				case seen <- struct{}{}:
				default:
				}
			}
			http.Error(w, "qualification endpoint", http.StatusNotFound)
		}),
	}
	// Serve on an already-open listener; close both listener and active conns
	// before removing the owned work directory, including timeout/error paths.
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	defer func() { _ = server.Close(); _ = listener.Close(); <-stopped }()
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe := gitClonePlan{url: "https://localhost:" + port + path, host: "localhost", port: port, address: net.ParseIP("127.0.0.2"), caFile: caPath}
	// Use the same transport flags and isolated runner as the real clone.
	args, err := pinnedGitCloneArgs(probe, home, filepath.Join(home, "unused"), "")
	if err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	for i, arg := range args {
		if arg == "clone" {
			args = append(args[:i], "ls-remote", "--", probe.url)
			break
		}
	}
	// Negative control catches a nonstandard localhost mapping or a local
	// listener which would make the pin unnecessary. Host processes and the
	// installed Git/helper/libraries remain trusted during this operation.
	negative := append([]string(nil), args...)
	removed := false
	for i, arg := range negative {
		if strings.HasPrefix(arg, "http.curloptResolve=") {
			negative = append(negative[:i-1], negative[i+1:]...)
			removed = true
			break
		}
	}
	if !removed {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	_, _ = runIsolatedGit(probeCtx, gitPath, home, negative...)
	if probeCtx.Err() != nil {
		return fmt.Errorf("git import transport qualification timed out")
	}
	select {
	case <-seen:
		return fmt.Errorf("git import transport qualification needs an isolated loopback endpoint")
	default:
	}
	_, _ = runIsolatedGit(probeCtx, gitPath, home, args...)
	if probeCtx.Err() != nil {
		return fmt.Errorf("git import transport qualification timed out")
	}
	// A deliberate404 is expected. Only an observed TLS request qualifies;
	// a successful process exit or accepted config string is not evidence.
	select {
	case <-seen:
		return nil
	default:
		return fmt.Errorf("git import transport cannot prove pinned HTTPS support; import refused")
	}
}

func gitProbeCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(5 * time.Minute), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certificate, err := tls.X509KeyPair(public, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	return certificate, public, err
}
