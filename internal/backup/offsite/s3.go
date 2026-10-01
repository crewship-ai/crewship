package offsite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// KindS3 is S3.Kind(): any S3-compatible store — AWS S3, Cloudflare R2,
// Backblaze B2 (S3 API), Wasabi, MinIO.
const KindS3 = "s3"

const (
	// DefaultPartSize is the multipart part size, and the threshold above
	// which Put switches from one PUT to a multipart upload. One part is
	// buffered in memory at a time.
	DefaultPartSize int64 = 64 << 20
	// MinPartSize is the S3 minimum for every part but the last.
	MinPartSize int64 = 5 << 20
	// maxParts is the S3 limit on parts per upload; Put grows the part size
	// for objects that would exceed it (64 MiB × 10 000 ≈ 640 GiB).
	maxParts = 10000

	// metaSHA256 is the user-metadata key carrying the object's SHA-256.
	// S3 returns it on HEAD/GET as x-amz-meta-sha256.
	metaSHA256Header = "X-Amz-Meta-Sha256"

	// The S3 flexible-checksum headers. A PUT / UPLOAD-PART carries the
	// SHA-256 of its body (base64) and the store refuses bytes that do not
	// match it (BadDigest); HEAD with checksum mode ENABLED returns the
	// store's own checksum of the object — composite "<b64>-<parts>" for a
	// multipart upload — which is what Upload verifies against.
	checksumSHA256Header    = "X-Amz-Checksum-Sha256"
	checksumAlgorithmHeader = "X-Amz-Checksum-Algorithm"
	sdkChecksumAlgHeader    = "X-Amz-Sdk-Checksum-Algorithm"
	checksumModeHeader      = "X-Amz-Checksum-Mode"

	maxErrorBody = 64 << 10
)

// S3Config is one S3-compatible destination as an admin configures it.
// AccessKeyID / SecretAccessKey come from the caller (a vault credential in
// the wired product) — the package never reads them from the environment.
type S3Config struct {
	// Endpoint is the service base URL, scheme included, no path:
	// "https://s3.eu-central-1.amazonaws.com",
	// "https://<account>.r2.cloudflarestorage.com",
	// "https://s3.us-west-004.backblazeb2.com", "http://minio.lan:9000".
	Endpoint string `json:"endpoint"`
	// Region is the signing region. Empty → "us-east-1"; R2 uses "auto".
	Region string `json:"region"`
	Bucket string `json:"bucket"`
	// Prefix is prepended to every key ("crewship/prod"). Optional.
	Prefix          string `json:"prefix,omitempty"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"-"`
	// PathStyle addresses the bucket as endpoint/bucket/key instead of
	// bucket.endpoint/key. MinIO and most self-hosted stores need it.
	PathStyle bool `json:"path_style"`
	// AllowPrivateNetwork lets the endpoint resolve to loopback, RFC 1918,
	// CGNAT or IPv6 ULA addresses (a MinIO on the LAN) and permits plain
	// http:// for such an endpoint. An instance admin must set it
	// explicitly. Cloud metadata, link-local, multicast and reserved ranges
	// stay blocked regardless — the same two-tier rule as crew
	// allow_private_endpoints and the Keeper judge endpoint.
	AllowPrivateNetwork bool `json:"allow_private_network"`
}

// String redacts the secret so a config can be logged or put in an error.
func (c S3Config) String() string {
	secret := ""
	if c.SecretAccessKey != "" {
		secret = "[redacted]"
	}
	return fmt.Sprintf("S3Config{Endpoint:%q Region:%q Bucket:%q Prefix:%q AccessKeyID:%q SecretAccessKey:%s PathStyle:%t AllowPrivateNetwork:%t}",
		c.Endpoint, c.Region, c.Bucket, c.Prefix, c.AccessKeyID, secret, c.PathStyle, c.AllowPrivateNetwork)
}

// GoString redacts the secret under %#v as well.
func (c S3Config) GoString() string { return c.String() }

var (
	bucketNameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,61}[A-Za-z0-9]$`)
	dnsBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

// Validate checks the config without touching the network. It runs the
// endpoint through httpsafe.ValidateURLForEndpoint: https only (http too when
// AllowPrivateNetwork), no userinfo, no localhost, no literal IP in a blocked
// range. DNS-resolved addresses are checked again at dial time.
func (c S3Config) Validate() error {
	if _, err := c.endpointURL(); err != nil {
		return err
	}
	if !bucketNameRE.MatchString(c.Bucket) {
		return fmt.Errorf("%w: bucket name %q", ErrInvalidConfig, c.Bucket)
	}
	if !c.PathStyle && !dnsBucketName.MatchString(c.Bucket) {
		return fmt.Errorf("%w: bucket %q is not a DNS label (dots, capitals or underscores); set path_style", ErrInvalidConfig, c.Bucket)
	}
	if p := strings.Trim(c.Prefix, "/"); p != "" {
		if err := ValidateKey(p); err != nil {
			return fmt.Errorf("%w: prefix: %v", ErrInvalidConfig, err)
		}
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return fmt.Errorf("%w: access_key_id and secret_access_key are required", ErrInvalidConfig)
	}
	if strings.ContainsAny(c.AccessKeyID, "/, \t\r\n") {
		return fmt.Errorf("%w: access_key_id contains a separator character", ErrInvalidConfig)
	}
	if strings.ContainsAny(c.Region, "/ \t\r\n") {
		return fmt.Errorf("%w: region %q", ErrInvalidConfig, c.Region)
	}
	return nil
}

func (c S3Config) endpointURL() (*url.URL, error) {
	schemes := []string{"https"}
	if c.AllowPrivateNetwork {
		schemes = append(schemes, "http")
	}
	u, err := httpsafe.ValidateURLForEndpoint(strings.TrimSpace(c.Endpoint), c.AllowPrivateNetwork, schemes...)
	if err != nil {
		return nil, fmt.Errorf("%w: endpoint: %v", ErrInvalidConfig, err)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: endpoint must be scheme://host[:port] with no path or query", ErrInvalidConfig)
	}
	return &url.URL{Scheme: strings.ToLower(u.Scheme), Host: u.Host}, nil
}

// RetryPolicy is exponential backoff with jitter for 5xx, 429, 408, S3
// RequestTimeout/SlowDown and transport errors (timeouts, resets). A blocked
// destination (SSRF guard) and 4xx answers are never retried.
type RetryPolicy struct {
	// MaxAttempts counts the first try. <= 1 disables retries.
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy: 5 attempts, 500 ms doubling to at most 30 s.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 5, BaseDelay: 500 * time.Millisecond, MaxDelay: 30 * time.Second}

func (p RetryPolicy) delay(attempt int) time.Duration {
	d := p.BaseDelay
	for i := 1; i < attempt && d < p.MaxDelay; i++ {
		d *= 2
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	if d <= 0 {
		return 0
	}
	// Equal jitter: [d/2, d). Parallel uploads that failed together do not
	// retry together.
	half := d / 2
	return half + time.Duration(mrand.Int64N(int64(half)+1))
}

// S3Option configures NewS3.
type S3Option func(*S3)

// WithPartSize sets the multipart part size (and single-PUT threshold). It
// must be at least MinPartSize.
func WithPartSize(n int64) S3Option { return func(s *S3) { s.partSize = n } }

// WithRetryPolicy replaces DefaultRetryPolicy.
func WithRetryPolicy(p RetryPolicy) S3Option { return func(s *S3) { s.retry = p } }

// WithClock sets the clock used for request signing and retry sleeps.
func WithClock(c Clock) S3Option { return func(s *S3) { s.clock = c } }

// S3 is a Destination backed by the S3 REST API, signed with SigV4.
type S3 struct {
	cfg      S3Config
	creds    sigV4Credentials
	base     *url.URL
	prefix   string
	client   *http.Client
	partSize int64
	retry    RetryPolicy
	clock    Clock
}

var _ Destination = (*S3)(nil)

// NewS3 validates cfg and returns a destination. No network I/O happens
// here; call Test to probe reachability and permissions.
//
// Outbound connections go through httpsafe's dialer: every resolved address
// is re-checked at connect time (DNS rebinding cannot swap it) against the
// hard tier always and the private tier unless cfg.AllowPrivateNetwork.
// Redirects are not followed, and environment proxies are not used.
func NewS3(cfg S3Config, opts ...S3Option) (*S3, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	base, _ := cfg.endpointURL()
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	s := &S3{
		cfg:      cfg,
		creds:    sigV4Credentials{accessKeyID: cfg.AccessKeyID, secretAccessKey: cfg.SecretAccessKey, region: region},
		base:     base,
		prefix:   strings.Trim(cfg.Prefix, "/"),
		partSize: DefaultPartSize,
		retry:    DefaultRetryPolicy,
		clock:    SystemClock{},
		client: &http.Client{
			Transport: &http.Transport{
				DialContext:           httpsafe.SafeDialContextForEndpoint(10*time.Second, cfg.AllowPrivateNetwork),
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 2 * time.Minute,
				ExpectContinueTimeout: time.Second,
				IdleConnTimeout:       60 * time.Second,
				MaxIdleConns:          8,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	for _, o := range opts {
		o(s)
	}
	if s.partSize < MinPartSize {
		return nil, fmt.Errorf("%w: part size %d below the S3 minimum %d", ErrInvalidConfig, s.partSize, MinPartSize)
	}
	if s.clock == nil {
		s.clock = SystemClock{}
	}
	return s, nil
}

// Kind returns KindS3.
func (s *S3) Kind() string { return KindS3 }

// Config returns the configuration (String redacts the secret).
func (s *S3) Config() S3Config { return s.cfg }

// S3Error is a non-2xx answer from the store. It never carries credentials.
type S3Error struct {
	Op         string // "PUT", "GET", "HEAD", "LIST", "DELETE", "CREATE-MULTIPART", …
	Key        string
	StatusCode int
	Code       string // S3 error code, e.g. "NoSuchKey", "SignatureDoesNotMatch"
	Message    string
	RequestID  string
}

func (e *S3Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "offsite: s3 %s", e.Op)
	if e.Key != "" {
		fmt.Fprintf(&b, " %s", e.Key)
	}
	fmt.Fprintf(&b, ": HTTP %d", e.StatusCode)
	if e.Code != "" {
		fmt.Fprintf(&b, " %s", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request id %s)", e.RequestID)
	}
	return b.String()
}

// Unwrap maps 404 / NoSuchKey to ErrNotFound so errors.Is works.
func (e *S3Error) Unwrap() error {
	if e.StatusCode == http.StatusNotFound && (e.Code == "" || e.Code == "NoSuchKey" || e.Code == "NotFound") {
		return ErrNotFound
	}
	return nil
}

func (e *S3Error) retryable() bool {
	switch e.Code {
	case "RequestTimeout", "SlowDown", "InternalError", "ServiceUnavailable":
		return true
	}
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests || e.StatusCode == http.StatusRequestTimeout
}

// fullKey joins the destination prefix and key.
func (s *S3) fullKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

// objectURL builds the request URL for key ("" addresses the bucket). Path
// holds the decoded key and RawPath its SigV4 encoding, so the transport
// sends exactly the bytes that were signed.
func (s *S3) objectURL(fullKey string, query url.Values) *url.URL {
	u := &url.URL{Scheme: s.base.Scheme, Host: s.base.Host}
	var p, raw string
	if s.cfg.PathStyle {
		p = "/" + s.cfg.Bucket
		raw = "/" + uriEncode(s.cfg.Bucket, true)
		if fullKey != "" {
			p += "/" + fullKey
			raw += "/" + uriEncode(fullKey, false)
		}
	} else {
		u.Host = s.cfg.Bucket + "." + s.base.Host
		p = "/" + fullKey
		raw = "/" + uriEncode(fullKey, false)
	}
	u.Path, u.RawPath = p, raw
	if len(query) > 0 {
		u.RawQuery = encodeQuery(query)
	}
	return u
}

// encodeQuery is url.Values.Encode with SigV4 escaping (space → %20).
func encodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

type request struct {
	op      string
	method  string
	key     string // caller key, for errors
	fullKey string // "" = bucket
	query   url.Values
	header  http.Header
	body    []byte
	sum     string // hex sha256 of body; computed when empty
}

// do sends req with signing and retries, returning a 2xx response whose body
// the caller must close. Non-2xx answers become *S3Error.
func (s *S3) do(ctx context.Context, r request) (*http.Response, error) {
	sum := r.sum
	if sum == "" {
		if len(r.body) == 0 {
			sum = emptySHA256
		} else {
			sum = hexSHA256(r.body)
		}
	}
	attempts := s.retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := s.clock.Sleep(ctx, s.retry.delay(attempt-1)); err != nil {
				return nil, err
			}
		}
		resp, err := s.once(ctx, r, sum)
		if err == nil {
			return resp, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (s *S3) once(ctx context.Context, r request, sum string) (*http.Response, error) {
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	u := s.objectURL(r.fullKey, r.query)
	req, err := http.NewRequestWithContext(ctx, r.method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("offsite: s3 %s: build request: %w", r.op, err)
	}
	// NewRequest re-parses the string; keep our URL so the escaping that
	// goes on the wire is exactly the one signed.
	req.URL = u
	for k, vs := range r.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	signV4(req, s.creds, sum, s.clock.Now())

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, &transportError{op: r.op, key: r.key, err: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, parseS3Error(r.op, r.key, resp)
}

// transportError wraps a network-level failure without the request (whose
// Authorization header must never reach a log line).
type transportError struct {
	op, key string
	err     error
}

func (e *transportError) Error() string {
	if e.key != "" {
		return fmt.Sprintf("offsite: s3 %s %s: %v", e.op, e.key, e.err)
	}
	return fmt.Sprintf("offsite: s3 %s: %v", e.op, e.err)
}
func (e *transportError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	var se *S3Error
	if errors.As(err, &se) {
		return se.retryable()
	}
	var te *transportError
	if errors.As(err, &te) {
		if errors.Is(err, httpsafe.ErrBlocked) || errors.Is(err, context.Canceled) {
			return false
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return false
		}
		return true
	}
	return false
}

func parseS3Error(op, key string, resp *http.Response) error {
	e := &S3Error{Op: op, Key: key, StatusCode: resp.StatusCode, RequestID: resp.Header.Get("X-Amz-Request-Id")}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var x struct {
		Code      string `xml:"Code"`
		Message   string `xml:"Message"`
		RequestID string `xml:"RequestId"`
	}
	if len(raw) > 0 && xml.Unmarshal(raw, &x) == nil {
		e.Code, e.Message = x.Code, x.Message
		if e.RequestID == "" {
			e.RequestID = x.RequestID
		}
	}
	return e
}

// Put stores size bytes from r. Objects up to the part size go in one PUT;
// larger ones use a multipart upload that is aborted on any failure,
// including ctx cancellation. Each request body is buffered and its SHA-256
// signed, so the store itself rejects bytes corrupted in transit. The bytes
// read from r are checked against size and sha256hex before the object is
// committed (single PUT: before sending; multipart: before Complete).
func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, sha256hex string) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}
	sum := strings.ToLower(sha256hex)
	if !isHexSHA256(sum) {
		return Object{}, fmt.Errorf("offsite: put %s: sha256 %q is not lowercase hex sha256", key, sha256hex)
	}
	if size < 0 {
		return Object{}, fmt.Errorf("offsite: put %s: negative size", key)
	}
	partSize := s.partSize
	if size <= partSize {
		return s.putSingle(ctx, key, r, size, sum)
	}
	if parts := (size + partSize - 1) / partSize; parts > maxParts {
		partSize = (size + maxParts - 1) / maxParts
		partSize = (partSize + (1<<20 - 1)) &^ (1<<20 - 1) // round up to MiB
	}
	return s.putMultipart(ctx, key, r, size, sum, partSize)
}

func (s *S3) putSingle(ctx context.Context, key string, r io.Reader, size int64, sum string) (Object, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return Object{}, fmt.Errorf("%w: %s: source shorter than %d bytes", ErrContentMismatch, key, size)
		}
		return Object{}, fmt.Errorf("offsite: put %s: read source: %w", key, err)
	}
	if err := expectEOF(r, key, size); err != nil {
		return Object{}, err
	}
	if got := hexSHA256(buf); got != sum {
		return Object{}, fmt.Errorf("%w: %s: source sha256 %s, declared %s", ErrContentMismatch, key, got, sum)
	}
	raw, _ := hex.DecodeString(sum)
	checksum := base64.StdEncoding.EncodeToString(raw)
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set(metaSHA256Header, sum)
	h.Set(sdkChecksumAlgHeader, "SHA256")
	h.Set(checksumSHA256Header, checksum)
	resp, err := s.do(ctx, request{op: "PUT", method: http.MethodPut, key: key, fullKey: s.fullKey(key), header: h, body: buf, sum: sum})
	if err != nil {
		return Object{}, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return Object{Key: key, Size: size, SHA256: sum, Checksum: checksum, ETag: resp.Header.Get("ETag"), Modified: s.clock.Now().UTC()}, nil
}

// expectEOF catches a source longer than declared (a file still growing).
func expectEOF(r io.Reader, key string, size int64) error {
	var one [1]byte
	n, err := r.Read(one[:])
	if n > 0 {
		return fmt.Errorf("%w: %s: source longer than %d bytes", ErrContentMismatch, key, size)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("offsite: put %s: read source: %w", key, err)
	}
	return nil
}

type completedPart struct {
	XMLName        xml.Name `xml:"Part"`
	PartNumber     int      `xml:"PartNumber"`
	ETag           string   `xml:"ETag"`
	ChecksumSHA256 string   `xml:"ChecksumSHA256,omitempty"`
}

func (s *S3) putMultipart(ctx context.Context, key string, r io.Reader, size int64, sum string, partSize int64) (obj Object, err error) {
	fk := s.fullKey(key)
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set(metaSHA256Header, sum)
	h.Set(checksumAlgorithmHeader, "SHA256")
	resp, err := s.do(ctx, request{op: "CREATE-MULTIPART", method: http.MethodPost, key: key, fullKey: fk, query: url.Values{"uploads": {""}}, header: h})
	if err != nil {
		return Object{}, err
	}
	var initRes struct {
		UploadID string `xml:"UploadId"`
	}
	derr := xml.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&initRes)
	_ = resp.Body.Close()
	if derr != nil || initRes.UploadID == "" {
		return Object{}, fmt.Errorf("offsite: s3 CREATE-MULTIPART %s: no UploadId in response", key)
	}
	uploadID := initRes.UploadID

	completed := false
	defer func() {
		if completed {
			return
		}
		// Abort on every failure path, cancellation included, with a fresh
		// bounded context: the parts already stored are billed until the
		// upload is aborted.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if aerr := s.abortMultipart(actx, key, fk, uploadID); aerr != nil {
			err = fmt.Errorf("%w (abort of upload %s also failed: %v)", err, uploadID, aerr)
		}
	}()

	whole := sha256.New()
	// composite hashes every part's raw SHA-256 in order: the store's
	// checksum of a multipart object is base64(sha256(those)) + "-N".
	composite := sha256.New()
	buf := make([]byte, partSize)
	var parts []completedPart
	var sent int64
	for n := 1; sent < size; n++ {
		if cerr := ctx.Err(); cerr != nil {
			return Object{}, cerr
		}
		chunk := partSize
		if rem := size - sent; rem < chunk {
			chunk = rem
		}
		b := buf[:chunk]
		if _, rerr := io.ReadFull(r, b); rerr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return Object{}, cerr
			}
			if errors.Is(rerr, io.ErrUnexpectedEOF) || errors.Is(rerr, io.EOF) {
				return Object{}, fmt.Errorf("%w: %s: source shorter than %d bytes", ErrContentMismatch, key, size)
			}
			return Object{}, fmt.Errorf("offsite: put %s: read source: %w", key, rerr)
		}
		whole.Write(b)
		partSum := sha256.Sum256(b)
		composite.Write(partSum[:])
		partChecksum := base64.StdEncoding.EncodeToString(partSum[:])
		ph := http.Header{}
		ph.Set(sdkChecksumAlgHeader, "SHA256")
		ph.Set(checksumSHA256Header, partChecksum)
		q := url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {uploadID}}
		presp, perr := s.do(ctx, request{op: "UPLOAD-PART", method: http.MethodPut, key: key, fullKey: fk, query: q, header: ph, body: b, sum: hex.EncodeToString(partSum[:])})
		if perr != nil {
			return Object{}, perr
		}
		etag := presp.Header.Get("ETag")
		_, _ = io.Copy(io.Discard, presp.Body)
		_ = presp.Body.Close()
		if etag == "" {
			return Object{}, fmt.Errorf("offsite: s3 UPLOAD-PART %s: part %d answered without an ETag", key, n)
		}
		parts = append(parts, completedPart{PartNumber: n, ETag: etag, ChecksumSHA256: partChecksum})
		sent += chunk
	}
	if eerr := expectEOF(r, key, size); eerr != nil {
		return Object{}, eerr
	}
	if got := hex.EncodeToString(whole.Sum(nil)); got != sum {
		return Object{}, fmt.Errorf("%w: %s: source sha256 %s, declared %s", ErrContentMismatch, key, got, sum)
	}

	checksum := base64.StdEncoding.EncodeToString(composite.Sum(nil)) + "-" + strconv.Itoa(len(parts))
	etag, cerr := s.completeMultipart(ctx, key, fk, uploadID, parts)
	if cerr != nil {
		// A Complete whose response was lost may have succeeded; the retry
		// then sees NoSuchUpload. Accept it only if the object is there
		// with the right size and checksum.
		var se *S3Error
		if errors.As(cerr, &se) && se.Code == "NoSuchUpload" {
			if o, herr := s.Head(ctx, key); herr == nil && o.Size == size && strings.EqualFold(o.SHA256, sum) {
				completed = true
				o.Checksum = checksum
				return o, nil
			}
		}
		return Object{}, cerr
	}
	completed = true
	return Object{Key: key, Size: size, SHA256: sum, Checksum: checksum, ETag: etag, Modified: s.clock.Now().UTC()}, nil
}

func (s *S3) completeMultipart(ctx context.Context, key, fk, uploadID string, parts []completedPart) (string, error) {
	body, err := xml.Marshal(struct {
		XMLName xml.Name        `xml:"CompleteMultipartUpload"`
		Parts   []completedPart `xml:"Part"`
	}{Parts: parts})
	if err != nil {
		return "", fmt.Errorf("offsite: s3 COMPLETE-MULTIPART %s: %w", key, err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/xml")
	resp, err := s.do(ctx, request{op: "COMPLETE-MULTIPART", method: http.MethodPost, key: key, fullKey: fk, query: url.Values{"uploadId": {uploadID}}, header: h, body: body})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	// S3 may answer 200 and still fail: the error is in the body.
	var res struct {
		XMLName xml.Name
		ETag    string `xml:"ETag"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(raw, &res) != nil {
		return "", fmt.Errorf("offsite: s3 COMPLETE-MULTIPART %s: unreadable response", key)
	}
	if res.XMLName.Local == "Error" {
		return "", &S3Error{Op: "COMPLETE-MULTIPART", Key: key, StatusCode: resp.StatusCode, Code: res.Code, Message: res.Message}
	}
	return res.ETag, nil
}

func (s *S3) abortMultipart(ctx context.Context, key, fk, uploadID string) error {
	resp, err := s.do(ctx, request{op: "ABORT-MULTIPART", method: http.MethodDelete, key: key, fullKey: fk, query: url.Values{"uploadId": {uploadID}}})
	if err != nil {
		var se *S3Error
		if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// Get opens key. Retries cover establishing the response only; a stream
// that breaks midway surfaces as a read error to the caller.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	if err := ValidateKey(key); err != nil {
		return nil, Object{}, err
	}
	resp, err := s.do(ctx, request{op: "GET", method: http.MethodGet, key: key, fullKey: s.fullKey(key)})
	if err != nil {
		return nil, Object{}, err
	}
	return resp.Body, objectFromHeaders(key, resp), nil
}

// Head returns key's metadata; a missing key wraps ErrNotFound.
func (s *S3) Head(ctx context.Context, key string) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}
	h := http.Header{}
	h.Set(checksumModeHeader, "ENABLED")
	resp, err := s.do(ctx, request{op: "HEAD", method: http.MethodHead, key: key, fullKey: s.fullKey(key), header: h})
	if err != nil {
		return Object{}, err
	}
	_ = resp.Body.Close()
	return objectFromHeaders(key, resp), nil
}

func objectFromHeaders(key string, resp *http.Response) Object {
	o := Object{
		Key:      key,
		Size:     resp.ContentLength,
		SHA256:   strings.ToLower(resp.Header.Get(metaSHA256Header)),
		Checksum: strings.TrimSpace(resp.Header.Get(checksumSHA256Header)),
		ETag:     resp.Header.Get("ETag"),
	}
	if o.Size < 0 {
		if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil {
			o.Size = n
		}
	}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		o.Modified = t.UTC()
	}
	return o
}

// Delete removes key; a missing key is not an error.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	resp, err := s.do(ctx, request{op: "DELETE", method: http.MethodDelete, key: key, fullKey: s.fullKey(key)})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// List pages through ListObjectsV2 for every key under prefix ("" = the
// whole destination) and returns them sorted, with the destination prefix
// stripped. SHA256 is not populated (listings carry no user metadata).
func (s *S3) List(ctx context.Context, prefix string) ([]Object, error) {
	if prefix != "" && (strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "\x00\r\n")) {
		return nil, fmt.Errorf("%w: list prefix %q", ErrInvalidKey, prefix)
	}
	full := prefix
	if s.prefix != "" {
		full = s.prefix + "/" + prefix
	}
	var out []Object
	token := ""
	for page := 0; ; page++ {
		if page > 100000 {
			return nil, errors.New("offsite: s3 LIST: too many pages")
		}
		q := url.Values{"list-type": {"2"}}
		if full != "" {
			q.Set("prefix", full)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do(ctx, request{op: "LIST", method: http.MethodGet, key: prefix, fullKey: "", query: q})
		if err != nil {
			return nil, err
		}
		var res struct {
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
			Contents              []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				ETag         string `xml:"ETag"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
		}
		derr := xml.NewDecoder(resp.Body).Decode(&res)
		_ = resp.Body.Close()
		if derr != nil {
			return nil, fmt.Errorf("offsite: s3 LIST: decode: %w", derr)
		}
		for _, c := range res.Contents {
			k := c.Key
			if s.prefix != "" {
				if !strings.HasPrefix(k, s.prefix+"/") {
					continue
				}
				k = strings.TrimPrefix(k, s.prefix+"/")
			}
			o := Object{Key: k, Size: c.Size, ETag: c.ETag}
			if t, err := time.Parse(time.RFC3339Nano, c.LastModified); err == nil {
				o.Modified = t.UTC()
			}
			out = append(out, o)
		}
		if !res.IsTruncated {
			break
		}
		if res.NextContinuationToken == "" || res.NextContinuationToken == token {
			return nil, errors.New("offsite: s3 LIST: truncated page without a new continuation token")
		}
		token = res.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// testKeyPrefix is where Test writes its probe object.
const testKeyPrefix = ".crewship-connection-test/"

// Test writes, reads back the metadata of, and deletes one tiny object. The
// returned error names the step that failed (upload / check / delete), which
// is what an admin needs to fix permissions.
func (s *S3) Test(ctx context.Context) error {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	key := testKeyPrefix + hex.EncodeToString(rnd[:])
	body := []byte("crewship off-site connection test\n")
	sum := hexSHA256(body)
	if _, err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), sum); err != nil {
		return fmt.Errorf("offsite: connection test: upload: %w", err)
	}
	o, err := s.Head(ctx, key)
	if err != nil {
		_ = s.Delete(ctx, key)
		return fmt.Errorf("offsite: connection test: check: %w", err)
	}
	if o.Size != int64(len(body)) || o.SHA256 != sum {
		_ = s.Delete(ctx, key)
		return fmt.Errorf("offsite: connection test: check: %w: stored object does not match (size %d, sha256 %q)", ErrVerifyFailed, o.Size, o.SHA256)
	}
	if err := s.Delete(ctx, key); err != nil {
		return fmt.Errorf("offsite: connection test: delete: %w", err)
	}
	return nil
}
