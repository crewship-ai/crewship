package update

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"regexp"
	"time"
)

// Cosign keyless signature verification for release artifacts.
//
// The release pipeline (goreleaser `signs:` block) signs every artifact with
// Sigstore cosign in keyless mode: GitHub Actions OIDC mints a short-lived
// Fulcio certificate whose SAN URI is the signing WORKFLOW's identity, and
// the artifact ships with `<name>.sig` (base64 ECDSA signature) and
// `<name>.pem` (base64-wrapped PEM certificate).
//
// Before this gate, self-update trusted checksums.txt fetched from the SAME
// release URL as the archive — sha256 verification therefore protected
// against corruption, not tampering: anyone able to serve a malicious
// archive could serve a matching checksums.txt. Verifying the cosign
// signature raises the bar to "attacker controls the repo's release
// workflow OIDC identity", the same trust anchor `cosign verify-blob`
// checks in the documented release-verification flow.
//
// Scope (deliberate): chain-of-trust to the embedded production Fulcio CA,
// workflow-identity pin, OIDC-issuer pin, and ECDSA signature over the
// payload. Rekor transparency-log inclusion is NOT checked — goreleaser's
// sign-blob output doesn't ship the inclusion proof next to the asset, and
// pulling in the full sigstore-go/TUF stack for it would add megabytes to
// a CLI whose value here is the identity pin. Because keyless certs live
// ~10 minutes, chain validity is anchored to the certificate's own NotBefore
// (see verifyChain) instead of time.Now.

// fulcioRoots holds the production Sigstore Fulcio root + intermediate,
// vendored from github.com/sigstore/root-signing (targets/fulcio_v1.crt.pem,
// targets/fulcio_intermediate_v1.crt.pem; both valid to 2031-10-05).
//
//go:embed fulcio_v1.crt.pem
var fulcioRootPEM []byte

//go:embed fulcio_intermediate_v1.crt.pem
var fulcioIntermediatePEM []byte

// fulcioOIDCIssuerOID is the Fulcio certificate extension carrying the OIDC
// issuer that authenticated the signing identity (raw string value).
// https://github.com/sigstore/fulcio/blob/main/docs/oid-info.md
var fulcioOIDCIssuerOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}

// fulcioOIDCIssuerV2OID is the DER-encoded successor of the issuer
// extension; newer Fulcio certs may carry only this one.
var fulcioOIDCIssuerV2OID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}

// releaseIdentityPattern pins the certificate SAN to this repo's release
// workflow — the identity `cosign verify-blob --certificate-identity-regexp`
// uses in the documented verification flow (release notes header). Stable
// releases are signed exclusively by release.yml; nightly prereleases are
// signed by nightly.yml and carry their own pin below — identityPatternForTag
// selects per channel, so neither channel ever accepts the other's identity.
var releaseIdentityPattern = regexp.MustCompile(
	`^https://github\.com/crewship-ai/crewship/\.github/workflows/release\.yml@`)

// nightlyIdentityPattern pins the nightly channel: nightly.yml (not
// release.yml) signs the checksums.txt of nightly-<date>-r<n> prereleases,
// so a nightly self-update target verifies against that workflow identity.
var nightlyIdentityPattern = regexp.MustCompile(
	`^https://github\.com/crewship-ai/crewship/\.github/workflows/nightly\.yml@`)

// identityPatternForTag returns the workflow identity allowed to have signed
// the release `tag`: nightly-<date>-r<n> tags → nightly.yml, everything else
// (semver release tags) → release.yml. Deliberately a per-channel pin rather
// than one combined pattern: a stable upgrade must never accept a
// nightly-signed checksums file, and vice versa.
func identityPatternForTag(tag string) *regexp.Regexp {
	if _, ok := parseNightlyVersion(tag); ok {
		return nightlyIdentityPattern
	}
	return releaseIdentityPattern
}

// githubActionsOIDCIssuer is the only issuer that may have authenticated
// the release workflow's identity.
const githubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"

// SignatureVerifyOptions parameterizes VerifyDetachedSignature. Zero values
// select the production defaults (embedded Fulcio roots, this repo's
// release.yml identity, GitHub Actions issuer); tests inject their own PKI.
type SignatureVerifyOptions struct {
	// Roots is the trusted CA pool. Nil → embedded production Fulcio root.
	Roots *x509.CertPool
	// Intermediates supplement chain building. Nil with nil Roots → the
	// embedded Fulcio intermediate; nil with non-nil Roots → empty.
	Intermediates *x509.CertPool
	// IdentityPattern pins the certificate's SAN URI. Nil → release.yml
	// (self-update swaps in the per-channel pin for nightly tags before
	// verifying; see identityPatternForTag).
	IdentityPattern *regexp.Regexp
	// OIDCIssuer pins the Fulcio issuer extension. "" → GitHub Actions.
	OIDCIssuer string
}

// VerifyDetachedSignature checks a cosign keyless detached signature:
// certificate chains to the trusted roots, its SAN identity and OIDC issuer
// match the pins, and the base64 ECDSA signature verifies over
// sha256(payload). sigB64 and certAsset are the raw bytes of the `.sig` and
// `.pem` release assets (both base64; the cert decodes to PEM — raw PEM is
// also accepted for robustness).
func VerifyDetachedSignature(payload, sigB64, certAsset []byte, opts SignatureVerifyOptions) error {
	cert, err := parseCertAsset(certAsset)
	if err != nil {
		return fmt.Errorf("parse signing certificate: %w", err)
	}
	if err := verifyChain(cert, opts); err != nil {
		return fmt.Errorf("signing certificate not trusted: %w", err)
	}
	if err := verifyIdentity(cert, opts); err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sigB64)))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("signing certificate key is %T, want ECDSA", cert.PublicKey)
	}
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return fmt.Errorf("signature does not match payload")
	}
	return nil
}

// parseCertAsset decodes a cosign `.pem` release asset: base64-wrapped PEM
// (what --output-certificate emits) or raw PEM.
func parseCertAsset(asset []byte) (*x509.Certificate, error) {
	data := bytes.TrimSpace(asset)
	if len(data) == 0 {
		return nil, fmt.Errorf("empty certificate")
	}
	if !bytes.HasPrefix(data, []byte("-----BEGIN")) {
		decoded, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return nil, fmt.Errorf("certificate is neither PEM nor base64(PEM): %w", err)
		}
		data = decoded
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("no CERTIFICATE block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// verifyChain validates the leaf against the trusted roots with the code
// signing EKU. CurrentTime is anchored just inside the certificate's own
// validity window: keyless certs live ~10 minutes and expire long before
// any self-update runs, and without a Rekor inclusion proof the actual
// signing instant is unprovable — the security load is carried by the
// chain-of-trust plus the identity pin, not by wall-clock freshness.
func verifyChain(leaf *x509.Certificate, opts SignatureVerifyOptions) error {
	roots := opts.Roots
	intermediates := opts.Intermediates
	if roots == nil {
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(fulcioRootPEM) {
			return fmt.Errorf("internal: embedded Fulcio root failed to parse")
		}
		if intermediates == nil {
			intermediates = x509.NewCertPool()
			if !intermediates.AppendCertsFromPEM(fulcioIntermediatePEM) {
				return fmt.Errorf("internal: embedded Fulcio intermediate failed to parse")
			}
		}
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		CurrentTime:   leaf.NotBefore.Add(time.Second),
	})
	return err
}

// verifyIdentity pins the certificate to the release workflow: the SAN URI
// must match the identity pattern and the Fulcio issuer extension must equal
// the expected OIDC issuer.
func verifyIdentity(cert *x509.Certificate, opts SignatureVerifyOptions) error {
	pattern := opts.IdentityPattern
	if pattern == nil {
		pattern = releaseIdentityPattern
	}
	issuer := opts.OIDCIssuer
	if issuer == "" {
		issuer = githubActionsOIDCIssuer
	}

	identityOK := false
	for _, uri := range cert.URIs {
		if pattern.MatchString(uri.String()) {
			identityOK = true
			break
		}
	}
	if !identityOK {
		got := make([]string, 0, len(cert.URIs))
		for _, uri := range cert.URIs {
			got = append(got, uri.String())
		}
		return fmt.Errorf("certificate identity %v does not match the pinned release workflow (%s)", got, pattern)
	}

	gotIssuer, err := certOIDCIssuer(cert)
	if err != nil {
		return err
	}
	if gotIssuer != issuer {
		return fmt.Errorf("certificate OIDC issuer %q, want %q", gotIssuer, issuer)
	}
	return nil
}

// certOIDCIssuer extracts the Fulcio OIDC-issuer extension: v1
// (1.3.6.1.4.1.57264.1.1, raw string) with v2 (…1.8, DER UTF8String)
// fallback.
func certOIDCIssuer(cert *x509.Certificate) (string, error) {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(fulcioOIDCIssuerOID) {
			return string(ext.Value), nil
		}
	}
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(fulcioOIDCIssuerV2OID) {
			var s string
			if _, err := asn1.Unmarshal(ext.Value, &s); err != nil {
				return "", fmt.Errorf("parse issuer-v2 extension: %w", err)
			}
			return s, nil
		}
	}
	return "", fmt.Errorf("certificate carries no Fulcio OIDC issuer extension")
}
