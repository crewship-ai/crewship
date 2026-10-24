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
	"sync/atomic"
	"time"
)

// A version/config listing cannot prove that the actual HTTPS helper's
// runtime libcurl honored CURLOPT_RESOLVE: Git ignores its setopt return.
// Qualify each invocation against two reserved owned endpoints. A wrong pin
// must reach the wrong endpoint; a correct pin must reach verified TLS. No
// repository is served and no external DNS name is needed.
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
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("git import transport qualification unavailable")
	}
	wrongIP, wrongSeen, closeWrong, err := gitProbeWrongEndpoint(port)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("git import transport qualification unavailable: %w", err)
	}
	defer closeWrong()
	var positiveContacts atomic.Int64
	listener = &gitProbeCountingListener{Listener: listener, accepts: &positiveContacts}
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
	probe := gitClonePlan{url: "https://localhost:" + port + path, host: "localhost", port: port, address: net.ParseIP("127.0.0.1"), caFile: caPath}
	makeArgs := func(plan gitClonePlan) ([]string, error) {
		args, err := pinnedGitCloneArgs(plan, home, filepath.Join(home, "unused"), "")
		if err != nil {
			return nil, err
		}
		for i, arg := range args {
			if arg == "clone" {
				return append(args[:i], "ls-remote", "--", plan.url), nil
			}
		}
		return nil, fmt.Errorf("missing clone command")
	}
	negative := probe
	negative.address = wrongIP
	negativeArgs, err := makeArgs(negative)
	if err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	negativeCtx, stopNegative := context.WithTimeout(ctx, 5*time.Second)
	_, _ = runIsolatedGit(negativeCtx, gitPath, home, negativeArgs...)
	negativeErr := negativeCtx.Err()
	stopNegative()
	if negativeErr != nil || positiveContacts.Load() != 0 {
		return fmt.Errorf("git import transport failed the negative pin control")
	}
	select {
	case <-wrongSeen:
	default:
		return fmt.Errorf("git import transport cannot prove the negative pin was honored")
	}
	args, err := makeArgs(probe)
	if err != nil {
		return fmt.Errorf("git import transport qualification unavailable")
	}
	positiveCtx, stopPositive := context.WithTimeout(ctx, 5*time.Second)
	defer stopPositive()
	_, _ = runIsolatedGit(positiveCtx, gitPath, home, args...)
	if positiveCtx.Err() != nil {
		return fmt.Errorf("git import transport qualification timed out")
	}
	// The owned endpoint deliberately returns404. A zero process exit or an
	// accepted config string cannot replace a nonce/Host/SNI-verified request.
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
