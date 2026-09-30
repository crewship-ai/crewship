package offsite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is an in-process S3 subset: single PUT, multipart (create / upload
// part / complete / abort), GET, HEAD, ListObjectsV2 with continuation,
// DELETE. Every request must carry a SigV4 Authorization header that verifies
// against the known secret — the verifier below is written independently of
// sigv4.go so a signing bug cannot hide behind a matching verification bug.
type fakeS3 struct {
	t         *testing.T
	bucket    string
	accessKey string
	secret    string
	region    string

	mu       sync.Mutex
	objects  map[string]*fakeObject
	uploads  map[string]*fakeUpload
	nextID   int
	requests map[string]int // op → count
	styles   map[string]int // "path" / "virtual" → count
	aborts   int
	authFail []string

	listPageSize int
	// inject runs before the op is served; returning status > 0 answers
	// with that error instead (after delay, if any). Called with mu NOT held.
	inject func(op string, attempt int, r *http.Request) (status int, code string, delay time.Duration)
	// onPart runs when an UPLOAD-PART arrives, before it is stored.
	onPart func(partNumber int, r *http.Request)
	// mutate alters an object right after it is stored (corruption tests).
	mutate func(key string, o *fakeObject)
	// completeError makes COMPLETE-MULTIPART answer 200 with an <Error> body.
	completeError bool
	// noChecksum makes the fake a store that ignores the S3 checksum
	// headers (as some S3-compatible stores do): nothing is checked on
	// upload and HEAD returns no x-amz-checksum-sha256.
	noChecksum bool
}

type fakeObject struct {
	data     []byte
	sha256   string
	etag     string
	modified time.Time
	// checksummed: uploaded with the SHA256 checksum algorithm, so HEAD with
	// x-amz-checksum-mode: ENABLED returns the store's checksum of the
	// object. partSizes are the multipart part lengths (nil: single PUT);
	// the store's checksum is then composite, "<b64>-<parts>".
	checksummed bool
	partSizes   []int
}

// providerChecksum is what a store that computes SHA-256 checksums itself
// reports for the object as it holds it (after any corruption a test
// injected): base64(sha256(data)), or for a multipart object the composite
// base64(sha256(sha256(part1) || … )) + "-N" over the stored bytes split at
// the part boundaries.
func (o *fakeObject) providerChecksum() string {
	if o.partSizes == nil {
		s := sha256.Sum256(o.data)
		return base64.StdEncoding.EncodeToString(s[:])
	}
	h := sha256.New()
	off := 0
	for _, n := range o.partSizes {
		end := off + n
		if end > len(o.data) {
			end = len(o.data)
		}
		if off > end {
			off = end
		}
		s := sha256.Sum256(o.data[off:end])
		h.Write(s[:])
		off += n
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(o.partSizes))
}

func b64SHA(b []byte) string {
	s := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(s[:])
}

type fakeUpload struct {
	key         string
	sha         string
	parts       map[int][]byte
	checksummed bool
}

func newFakeS3(t *testing.T) *fakeS3 {
	return &fakeS3{
		t: t, bucket: "backups", accessKey: "AKIDTEST", secret: "s3cr3t/KEY+value", region: "eu-test-1",
		objects: map[string]*fakeObject{}, uploads: map[string]*fakeUpload{},
		requests: map[string]int{}, styles: map[string]int{}, listPageSize: 1000,
	}
}

func (f *fakeS3) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[op]
}

func (f *fakeS3) object(key string) (*fakeObject, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	return o, ok
}

func writeS3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("X-Amz-Request-Id", "req-"+code)
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>fake %s</Message></Error>`, code, code)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeS3Error(w, 400, "IncompleteBody")
		return
	}
	if reason := f.verifySigV4(r, body); reason != "" {
		f.mu.Lock()
		f.authFail = append(f.authFail, reason)
		f.mu.Unlock()
		writeS3Error(w, 403, "SignatureDoesNotMatch")
		return
	}

	// Bucket addressing.
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	var key string
	style := "path"
	if strings.HasPrefix(host, f.bucket+".") {
		style = "virtual"
		key = strings.TrimPrefix(r.URL.Path, "/")
	} else {
		p := strings.TrimPrefix(r.URL.Path, "/")
		b, rest, _ := strings.Cut(p, "/")
		if b != f.bucket {
			writeS3Error(w, 404, "NoSuchBucket")
			return
		}
		key = rest
	}
	q := r.URL.Query()
	op := classify(r.Method, key, q)

	f.mu.Lock()
	f.styles[style]++
	f.requests[op]++
	attempt := f.requests[op]
	inject := f.inject
	f.mu.Unlock()

	if inject != nil {
		if status, code, delay := inject(op, attempt, r); status > 0 || delay > 0 {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-r.Context().Done():
					return
				}
			}
			if status > 0 {
				writeS3Error(w, status, code)
				return
			}
		}
	}

	// A store that honours checksums refuses a body whose declared
	// x-amz-checksum-sha256 does not match what arrived (BadDigest).
	if want := r.Header.Get("X-Amz-Checksum-Sha256"); want != "" && !f.noChecksum && (op == "PUT" || op == "UPLOAD-PART") {
		if want != b64SHA(body) {
			writeS3Error(w, 400, "BadDigest")
			return
		}
	}

	switch op {
	case "PUT":
		checksummed := !f.noChecksum && r.Header.Get("X-Amz-Checksum-Sha256") != ""
		f.putObject(w, key, body, r.Header.Get("X-Amz-Meta-Sha256"), checksummed)
	case "CREATE-MULTIPART":
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("upload-%d", f.nextID)
		f.uploads[id] = &fakeUpload{key: key, sha: r.Header.Get("X-Amz-Meta-Sha256"), parts: map[int][]byte{},
			checksummed: !f.noChecksum && strings.EqualFold(r.Header.Get("X-Amz-Checksum-Algorithm"), "SHA256")}
		f.mu.Unlock()
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, f.bucket, xmlEscape(key), id)
	case "UPLOAD-PART":
		n, _ := strconv.Atoi(q.Get("partNumber"))
		if f.onPart != nil {
			f.onPart(n, r)
			if r.Context().Err() != nil {
				return
			}
		}
		f.mu.Lock()
		up, ok := f.uploads[q.Get("uploadId")]
		if ok {
			up.parts[n] = body
		}
		f.mu.Unlock()
		if !ok {
			writeS3Error(w, 404, "NoSuchUpload")
			return
		}
		w.Header().Set("ETag", `"`+hexSHA(body)[:32]+`"`)
	case "COMPLETE-MULTIPART":
		f.completeMultipart(w, q.Get("uploadId"), body)
	case "ABORT-MULTIPART":
		f.mu.Lock()
		_, ok := f.uploads[q.Get("uploadId")]
		delete(f.uploads, q.Get("uploadId"))
		f.aborts++
		f.mu.Unlock()
		if !ok {
			writeS3Error(w, 404, "NoSuchUpload")
			return
		}
		w.WriteHeader(204)
	case "GET", "HEAD":
		o, ok := f.object(key)
		if !ok {
			if op == "HEAD" {
				w.WriteHeader(404)
				return
			}
			writeS3Error(w, 404, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.Header().Set("ETag", o.etag)
		w.Header().Set("Last-Modified", o.modified.Format(http.TimeFormat))
		if o.sha256 != "" {
			w.Header().Set("X-Amz-Meta-Sha256", o.sha256)
		}
		if o.checksummed && strings.EqualFold(r.Header.Get("X-Amz-Checksum-Mode"), "ENABLED") {
			w.Header().Set("X-Amz-Checksum-Sha256", o.providerChecksum())
			if o.partSizes != nil {
				w.Header().Set("X-Amz-Checksum-Type", "COMPOSITE")
			} else {
				w.Header().Set("X-Amz-Checksum-Type", "FULL_OBJECT")
			}
		}
		if op == "GET" {
			_, _ = w.Write(o.data)
		}
	case "DELETE":
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(204)
	case "LIST":
		f.list(w, q)
	default:
		writeS3Error(w, 400, "UnsupportedOperation")
	}
}

func classify(method, key string, q url.Values) string {
	switch {
	case method == http.MethodGet && key == "" && q.Get("list-type") == "2":
		return "LIST"
	case method == http.MethodPost && q.Has("uploads"):
		return "CREATE-MULTIPART"
	case method == http.MethodPut && q.Has("uploadId"):
		return "UPLOAD-PART"
	case method == http.MethodPost && q.Has("uploadId"):
		return "COMPLETE-MULTIPART"
	case method == http.MethodDelete && q.Has("uploadId"):
		return "ABORT-MULTIPART"
	}
	return method
}

func (f *fakeS3) store(key string, data []byte, sha string, checksummed bool, partSizes []int) {
	o := &fakeObject{data: data, sha256: sha, etag: `"` + hexSHA(data)[:32] + `"`, modified: time.Now().UTC().Truncate(time.Second),
		checksummed: checksummed, partSizes: partSizes}
	if f.mutate != nil {
		f.mutate(key, o)
	}
	f.objects[key] = o
}

func (f *fakeS3) putObject(w http.ResponseWriter, key string, body []byte, sha string, checksummed bool) {
	f.mu.Lock()
	f.store(key, body, sha, checksummed, nil)
	etag := f.objects[key].etag
	f.mu.Unlock()
	w.Header().Set("ETag", etag)
	if checksummed {
		w.Header().Set("X-Amz-Checksum-Sha256", b64SHA(body))
	}
}

func (f *fakeS3) completeMultipart(w http.ResponseWriter, id string, body []byte) {
	var req struct {
		Parts []struct {
			PartNumber     int
			ETag           string
			ChecksumSHA256 string
		} `xml:"Part"`
	}
	if err := xml.Unmarshal(body, &req); err != nil || len(req.Parts) == 0 {
		writeS3Error(w, 400, "MalformedXML")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	up, ok := f.uploads[id]
	if !ok {
		writeS3Error(w, 404, "NoSuchUpload")
		return
	}
	if f.completeError {
		w.WriteHeader(200)
		fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>InternalError</Code><Message>complete failed after 200</Message></Error>`)
		return
	}
	var data []byte
	var sizes []int
	for i, p := range req.Parts {
		part, ok := up.parts[p.PartNumber]
		if !ok || p.PartNumber != i+1 || p.ETag != `"`+hexSHA(part)[:32]+`"` {
			writeS3Error(w, 400, "InvalidPart")
			return
		}
		if up.checksummed && p.ChecksumSHA256 != b64SHA(part) {
			writeS3Error(w, 400, "InvalidPart")
			return
		}
		data = append(data, part...)
		sizes = append(sizes, len(part))
	}
	delete(f.uploads, id)
	if !up.checksummed {
		sizes = nil
	}
	f.store(up.key, data, up.sha, up.checksummed, sizes)
	fmt.Fprintf(w, `<CompleteMultipartUploadResult><Key>%s</Key><ETag>"mp-%d"</ETag></CompleteMultipartUploadResult>`, xmlEscape(up.key), len(req.Parts))
}

func (f *fakeS3) list(w http.ResponseWriter, q url.Values) {
	prefix := q.Get("prefix")
	after := q.Get("continuation-token")
	f.mu.Lock()
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	truncated := false
	if len(keys) > f.listPageSize {
		keys, truncated = keys[:f.listPageSize], true
	}
	type content struct {
		Key          string
		Size         int
		ETag         string
		LastModified string
	}
	res := struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		Name                  string
		Prefix                string
		KeyCount              int
		IsTruncated           bool
		NextContinuationToken string `xml:",omitempty"`
		Contents              []content
	}{Name: f.bucket, Prefix: prefix, KeyCount: len(keys), IsTruncated: truncated}
	for _, k := range keys {
		o := f.objects[k]
		res.Contents = append(res.Contents, content{k, len(o.data), o.etag, o.modified.Format(time.RFC3339)})
	}
	if truncated {
		res.NextContinuationToken = keys[len(keys)-1]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var authRE = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([^/]+)/(\d{8})/([^/]+)/s3/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)

// verifySigV4 recomputes the signature from what arrived on the wire and
// returns "" when it matches, otherwise the reason.
func (f *fakeS3) verifySigV4(r *http.Request, body []byte) string {
	m := authRE.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		return "malformed Authorization: " + r.Header.Get("Authorization")
	}
	ak, day, region, signed, sig := m[1], m[2], m[3], m[4], m[5]
	if ak != f.accessKey {
		return "wrong access key"
	}
	if region != f.region {
		return "wrong region " + region
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if len(amzDate) != 16 || amzDate[:8] != day {
		return "x-amz-date does not match credential scope"
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload != hexSHA(body) {
		return "x-amz-content-sha256 does not match the body"
	}
	names := strings.Split(signed, ";")
	for _, must := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
		found := false
		for _, n := range names {
			found = found || n == must
		}
		if !found {
			return "SignedHeaders lacks " + must
		}
	}
	var ch strings.Builder
	for _, n := range names {
		v := r.Header.Get(n)
		if n == "host" {
			v = r.Host
		}
		ch.WriteString(n + ":" + strings.TrimSpace(v) + "\n")
	}
	rawPath, rawQuery, _ := strings.Cut(r.RequestURI, "?")
	creq := strings.Join([]string{r.Method, rawPath, fakeCanonicalQuery(rawQuery), ch.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(creq))
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	mac := func(k []byte, d string) []byte { h := hmac.New(sha256.New, k); h.Write([]byte(d)); return h.Sum(nil) }
	k := mac(mac(mac(mac([]byte("AWS4"+f.secret), day), region), "s3"), "aws4_request")
	want := hex.EncodeToString(mac(k, sts))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "signature mismatch; canonical request was:\n" + creq
	}
	return ""
}

func fakeCanonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return "!invalid"
	}
	var parts []string
	for k, vs := range vals {
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func awsEscape(s string) string {
	// url.QueryEscape plus the AWS differences: space is %20, '~' is kept.
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

// ---- test wiring ----

type fakeServer struct {
	*fakeS3
	srv *httptest.Server
}

func startFake(t *testing.T) *fakeServer {
	t.Helper()
	f := newFakeS3(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &fakeServer{fakeS3: f, srv: srv}
}

// testClient points s at the fake regardless of the Host being addressed, so
// virtual-host URLs (bucket.s3.test) work without DNS.
func (fs *fakeServer) testClient(headerTimeout time.Duration) *http.Client {
	addr := fs.srv.Listener.Addr().String()
	d := &net.Dialer{Timeout: 2 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
			ResponseHeaderTimeout: headerTimeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type s3Opts struct {
	pathStyle     bool
	prefix        string
	partSize      int64
	headerTimeout time.Duration
	attempts      int
}

func (fs *fakeServer) newS3(t *testing.T, o s3Opts) *S3 {
	t.Helper()
	_, port, _ := net.SplitHostPort(fs.srv.Listener.Addr().String())
	cfg := S3Config{
		Endpoint: "http://s3.test:" + port, Region: fs.region, Bucket: fs.bucket, Prefix: o.prefix,
		AccessKeyID: fs.accessKey, SecretAccessKey: fs.secret, PathStyle: o.pathStyle, AllowPrivateNetwork: true,
	}
	attempts := o.attempts
	if attempts == 0 {
		attempts = 4
	}
	s, err := NewS3(cfg, WithRetryPolicy(RetryPolicy{MaxAttempts: attempts, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}))
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	if o.headerTimeout == 0 {
		o.headerTimeout = 5 * time.Second
	}
	s.client = fs.testClient(o.headerTimeout)
	if o.partSize > 0 {
		s.partSize = o.partSize
	}
	t.Cleanup(func() {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if len(fs.authFail) > 0 {
			t.Errorf("fake S3 rejected %d signature(s); first: %s", len(fs.authFail), fs.authFail[0])
		}
	})
	return s
}
