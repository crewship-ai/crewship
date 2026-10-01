package offsite

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 for the S3 REST API, standard library only.
//
// The repository had no SigV4 code to reuse (the Bedrock path signs in the
// sidecar's upstream, not here), and pulling aws-sdk-go-v2 or minio-go in for
// seven REST calls would add a large dependency tree to a single-binary
// product. The scheme is small and fully specified:
// https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html
//
// Everything below is pure (no I/O, no clock) so the known-answer vectors from
// the AWS documentation pin it in sigv4_test.go.

const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	sigV4Service   = "s3"
	amzDateLayout  = "20060102T150405Z"
	amzDayLayout   = "20060102"

	// Note: we never send UNSIGNED-PAYLOAD. Every body is buffered and
	// hashed first, so the signed x-amz-content-sha256 makes the server
	// reject a body corrupted in transit.

	// emptySHA256 is sha256("") — the payload hash of every bodiless request.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// sigV4Credentials is the secret half of the signer. It is never formatted,
// logged or returned; S3Config.String redacts it at the config layer.
type sigV4Credentials struct {
	accessKeyID     string
	secretAccessKey string
	region          string
}

// signV4 adds x-amz-date, x-amz-content-sha256 and Authorization to req.
// payloadHash is the lowercase hex sha256 of the body that will be sent.
//
// Signed headers: host, every x-amz-* header, and content-type / content-md5
// / range when present. Other headers (User-Agent, Content-Length,
// Accept-Encoding) may be altered by the transport and are left unsigned,
// as the AWS SDKs do.
func signV4(req *http.Request, creds sigV4Credentials, payloadHash string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format(amzDateLayout)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	canonicalHeaders, signedHeaders := canonicalHeaderBlock(req.Header, host)
	creq := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL.RawQuery),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{now.Format(amzDayLayout), creds.region, sigV4Service, "aws4_request"}, "/")
	sts := strings.Join([]string{sigV4Algorithm, amzDate, scope, hexSHA256([]byte(creq))}, "\n")
	sig := hex.EncodeToString(hmacSHA256(signingKey(creds.secretAccessKey, now.Format(amzDayLayout), creds.region), []byte(sts)))

	req.Header.Set("Authorization", sigV4Algorithm+
		" Credential="+creds.accessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+sig)
}

func signingKey(secret, day, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(day))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(sigV4Service))
	return hmacSHA256(k, []byte("aws4_request"))
}

// canonicalURI is the request path exactly as it goes on the wire. S3 (unlike
// other AWS services) does not normalise or double-encode it, so we sign the
// same escaped form the transport sends — url.URL.EscapedPath returns RawPath
// when it is a valid encoding of Path, which buildURL guarantees.
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

// canonicalQuery sorts the query by key (then value) and re-encodes each part
// with the SigV4 rules (RFC 3986 unreserved characters kept, space as %20).
// A key with no value is written as "key=".
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		// Decode what the caller encoded, then re-encode canonically. A
		// malformed escape is signed verbatim; the server will then compute
		// a different signature and refuse, which is the right outcome.
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		pairs = append(pairs, kv{uriEncode(k, true), uriEncode(v, true)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

func canonicalHeaderBlock(h http.Header, host string) (canonical, signed string) {
	vals := map[string]string{"host": strings.TrimSpace(host)}
	for name, vs := range h {
		ln := strings.ToLower(name)
		if !isSignedHeader(ln) {
			continue
		}
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		vals[ln] = strings.Join(trimmed, ",")
	}
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(vals[n])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

func isSignedHeader(lower string) bool {
	switch lower {
	case "host", "content-type", "content-md5", "range":
		return true
	case "authorization":
		return false
	}
	return strings.HasPrefix(lower, "x-amz-")
}

// uriEncode percent-encodes every byte except the RFC 3986 unreserved set.
// encodeSlash=false keeps '/' literal, which is what object-key paths need.
func uriEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
