package skills

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net"
	"strings"
	"testing"
)

func TestGitProbeCertificateSeparatesPrivateRootAndServer(t *testing.T) {
	for _, ips := range [][]net.IP{nil, {net.ParseIP("10.1.2.3")}} {
		certificate, publicCA, err := gitProbeCertificate(ips...)
		if err != nil {
			t.Fatal(err)
		}
		if len(certificate.Certificate) != 2 {
			t.Fatalf("expected leaf and separate root, got %d certificates", len(certificate.Certificate))
		}
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		root, err := x509.ParseCertificate(certificate.Certificate[1])
		if err != nil {
			t.Fatal(err)
		}
		if leaf.IsCA || !root.IsCA || !root.BasicConstraintsValid || root.KeyUsage&x509.KeyUsageCertSign == 0 {
			t.Fatal("server and CA roles are not separate")
		}
		if leaf.Subject.String() == "" || root.Subject.String() == "" || leaf.SerialNumber.Cmp(root.SerialNumber) == 0 || bytes.Equal(leaf.RawSubjectPublicKeyInfo, root.RawSubjectPublicKeyInfo) {
			t.Fatal("distinct subjects, serials and keys are required")
		}
		block, rest := pem.Decode(publicCA)
		if block == nil || len(rest) != 0 || !bytes.Equal(block.Bytes, root.Raw) {
			t.Fatal("private trust file must contain only the root")
		}
		roots := x509.NewCertPool()
		roots.AddCert(root)
		names := []string{"localhost"}
		for _, ip := range ips {
			names = append(names, ip.String())
		}
		for _, name := range names {
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
				t.Fatalf("owned server certificate for %s: %v", name, err)
			}
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "different.example.invalid"}); err == nil {
			t.Fatal("wrong host accepted")
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: x509.NewCertPool(), DNSName: "localhost"}); err == nil {
			t.Fatal("untrusted issuer accepted")
		}
	}
}

func TestGitProbeFailureKeepsOwnedDiagnosticsOutOfAPIMessage(t *testing.T) {
	err := &gitProbeFailure{output: "owned diagnostic marker", positiveContacts: 1}
	if strings.Contains(err.Error(), "diagnostic") || err.Error() != "git import transport cannot prove pinned HTTPS support; import refused" {
		t.Fatalf("unexpected public diagnostic: %s", err.Error())
	}
}
