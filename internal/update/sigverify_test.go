package update

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// ─── test PKI ────────────────────────────────────────────────────────────

type testPKI struct {
	rootPool *x509.CertPool
	leafPEM  []byte // PEM-encoded leaf certificate
	leafKey  *ecdsa.PrivateKey
}

// newTestPKI builds a one-root chain with a Fulcio-shaped leaf: URI SAN =
// identity, OIDC-issuer extension (1.3.6.1.4.1.57264.1.1) = issuer, code
// signing EKU, 10-minute validity — the same shape cosign keyless emits.
func newTestPKI(t *testing.T, identity, issuer string) *testPKI {
	t.Helper()

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(identity)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs:         []*url.URL{uri},
	}
	if issuer != "" {
		leafTmpl.ExtraExtensions = []pkix.Extension{{
			Id:    fulcioOIDCIssuerOID,
			Value: []byte(issuer),
		}}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, rootCert, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(rootCert)
	return &testPKI{
		rootPool: pool,
		leafPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		leafKey:  leafKey,
	}
}

func (p *testPKI) sign(t *testing.T, payload []byte) string {
	t.Helper()
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, p.leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

const (
	testIdentity = "https://github.com/test-org/test-repo/.github/workflows/release.yml@refs/tags/v1.0.0"
	testIssuer   = "https://token.actions.githubusercontent.com"
)

func testOpts(p *testPKI) SignatureVerifyOptions {
	return SignatureVerifyOptions{
		Roots:           p.rootPool,
		IdentityPattern: regexp.MustCompile(`^https://github\.com/test-org/test-repo/\.github/workflows/release\.yml@`),
		OIDCIssuer:      testIssuer,
	}
}

// ─── tests ───────────────────────────────────────────────────────────────

func TestVerifyDetachedSignature_HappyPath(t *testing.T) {
	pki := newTestPKI(t, testIdentity, testIssuer)
	payload := []byte("abc123  crewship_1.0.0_linux_amd64.tar.gz\n")
	sig := pki.sign(t, payload)

	if err := VerifyDetachedSignature(payload, []byte(sig), pki.leafPEM, testOpts(pki)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyDetachedSignature_TamperedPayload(t *testing.T) {
	pki := newTestPKI(t, testIdentity, testIssuer)
	payload := []byte("abc123  crewship_1.0.0_linux_amd64.tar.gz\n")
	sig := pki.sign(t, payload)

	tampered := []byte("evil00  crewship_1.0.0_linux_amd64.tar.gz\n")
	if err := VerifyDetachedSignature(tampered, []byte(sig), pki.leafPEM, testOpts(pki)); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestVerifyDetachedSignature_WrongIdentity(t *testing.T) {
	pki := newTestPKI(t, "https://github.com/attacker/repo/.github/workflows/release.yml@refs/tags/v9", testIssuer)
	payload := []byte("payload")
	sig := pki.sign(t, payload)

	err := VerifyDetachedSignature(payload, []byte(sig), pki.leafPEM, testOpts(pki))
	if err == nil {
		t.Fatal("certificate with foreign workflow identity accepted")
	}
}

func TestVerifyDetachedSignature_WrongIssuer(t *testing.T) {
	pki := newTestPKI(t, testIdentity, "https://accounts.google.com")
	payload := []byte("payload")
	sig := pki.sign(t, payload)

	if err := VerifyDetachedSignature(payload, []byte(sig), pki.leafPEM, testOpts(pki)); err == nil {
		t.Fatal("certificate with foreign OIDC issuer accepted")
	}
}

func TestVerifyDetachedSignature_MissingIssuerExt(t *testing.T) {
	pki := newTestPKI(t, testIdentity, "")
	payload := []byte("payload")
	sig := pki.sign(t, payload)

	if err := VerifyDetachedSignature(payload, []byte(sig), pki.leafPEM, testOpts(pki)); err == nil {
		t.Fatal("certificate without OIDC issuer extension accepted")
	}
}

func TestVerifyDetachedSignature_UntrustedRoot(t *testing.T) {
	pki := newTestPKI(t, testIdentity, testIssuer)
	other := newTestPKI(t, testIdentity, testIssuer) // different root
	payload := []byte("payload")
	sig := pki.sign(t, payload)

	opts := testOpts(other) // verifier trusts OTHER's root, not pki's
	if err := VerifyDetachedSignature(payload, []byte(sig), pki.leafPEM, opts); err == nil {
		t.Fatal("certificate chaining to an untrusted root accepted")
	}
}

func TestVerifyDetachedSignature_GarbageInputs(t *testing.T) {
	pki := newTestPKI(t, testIdentity, testIssuer)
	payload := []byte("payload")
	sig := pki.sign(t, payload)

	cases := []struct {
		name string
		sig  []byte
		cert []byte
	}{
		{"garbage sig", []byte("!!!not-base64!!!"), pki.leafPEM},
		{"empty sig", nil, pki.leafPEM},
		{"garbage cert", []byte(sig), []byte("not a cert")},
		{"empty cert", []byte(sig), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyDetachedSignature(payload, tc.sig, tc.cert, testOpts(pki)); err == nil {
				t.Fatal("garbage input accepted")
			}
		})
	}
}

// TestVerifyDetachedSignature_Base64WrappedCert covers the actual asset
// format: cosign's --output-certificate writes base64(PEM), not raw PEM.
func TestVerifyDetachedSignature_Base64WrappedCert(t *testing.T) {
	pki := newTestPKI(t, testIdentity, testIssuer)
	payload := []byte("payload")
	sig := pki.sign(t, payload)
	wrapped := []byte(base64.StdEncoding.EncodeToString(pki.leafPEM))

	if err := VerifyDetachedSignature(payload, []byte(sig), wrapped, testOpts(pki)); err != nil {
		t.Fatalf("base64-wrapped PEM cert rejected: %v", err)
	}
}

// TestVerifyDetachedSignature_RealArtifacts verifies a REAL cosign keyless
// signature produced by the repo's CI (nightly-20260712-r434 checksums.txt)
// against the embedded production Fulcio roots — an offline end-to-end
// regression for the exact bytes GitHub serves. Identity is overridden to
// the nightly workflow (stable releases are signed by release.yml, which is
// the compiled-in default).
func TestVerifyDetachedSignature_RealArtifacts(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return data
	}
	payload := read("real_checksums.txt")
	sig := read("real_checksums.txt.sig")
	cert := read("real_checksums.txt.pem")

	opts := SignatureVerifyOptions{ // empty Roots → embedded Fulcio roots
		IdentityPattern: regexp.MustCompile(`^https://github\.com/crewship-ai/crewship/\.github/workflows/nightly\.yml@`),
	}
	if err := VerifyDetachedSignature(payload, sig, cert, opts); err != nil {
		t.Fatalf("real cosign artifacts failed verification: %v", err)
	}

	// The default (release.yml) identity must REJECT the nightly cert —
	// proves the identity pin actually pins.
	if err := VerifyDetachedSignature(payload, sig, cert, SignatureVerifyOptions{}); err == nil {
		t.Fatal("nightly-signed cert accepted under the release.yml identity pin")
	}

	// Tampered checksums must fail against the real signature.
	tampered := append([]byte("00"), payload[2:]...)
	if err := VerifyDetachedSignature(tampered, sig, cert, opts); err == nil {
		t.Fatal("tampered real checksums accepted")
	}
}
