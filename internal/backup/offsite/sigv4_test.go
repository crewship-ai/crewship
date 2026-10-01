package offsite

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Known-answer vectors from the AWS S3 SigV4 documentation
// (sig-v4-header-based-auth.html, "Examples: Signature Calculations").
func TestSignV4KnownAnswers(t *testing.T) {
	creds := sigV4Credentials{
		accessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		secretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		region:          "us-east-1",
	}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		url        string
		header     map[string]string
		wantSigned string
		wantSig    string
	}{
		{
			name:       "GET object with Range",
			url:        "https://examplebucket.s3.amazonaws.com/test.txt",
			header:     map[string]string{"Range": "bytes=0-9"},
			wantSigned: "host;range;x-amz-content-sha256;x-amz-date",
			wantSig:    "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name:       "GET bucket lifecycle (valueless query key)",
			url:        "https://examplebucket.s3.amazonaws.com/?lifecycle",
			wantSigned: "host;x-amz-content-sha256;x-amz-date",
			wantSig:    "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name:       "ListObjects with unsorted query",
			url:        "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			wantSigned: "host;x-amz-content-sha256;x-amz-date",
			wantSig:    "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			signV4(req, creds, emptySHA256, at)
			auth := req.Header.Get("Authorization")
			wantPrefix := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=" + tc.wantSigned + ", Signature="
			if !strings.HasPrefix(auth, wantPrefix) {
				t.Fatalf("Authorization = %q\nwant prefix %q", auth, wantPrefix)
			}
			if got := strings.TrimPrefix(auth, wantPrefix); got != tc.wantSig {
				t.Fatalf("signature = %s, want %s", got, tc.wantSig)
			}
			if got := req.Header.Get("X-Amz-Date"); got != "20130524T000000Z" {
				t.Fatalf("x-amz-date = %q", got)
			}
		})
	}
}

func TestURIEncode(t *testing.T) {
	cases := []struct {
		in, slashKept, slashEncoded string
	}{
		{"a/b", "a/b", "a%2Fb"},
		{"a b+c=d", "a%20b%2Bc%3Dd", "a%20b%2Bc%3Dd"},
		{"~._-AZaz09", "~._-AZaz09", "~._-AZaz09"},
		{"é", "%C3%A9", "%C3%A9"},
		{"%?#&", "%25%3F%23%26", "%25%3F%23%26"},
	}
	for _, tc := range cases {
		if got := uriEncode(tc.in, false); got != tc.slashKept {
			t.Errorf("uriEncode(%q, false) = %q, want %q", tc.in, got, tc.slashKept)
		}
		if got := uriEncode(tc.in, true); got != tc.slashEncoded {
			t.Errorf("uriEncode(%q, true) = %q, want %q", tc.in, got, tc.slashEncoded)
		}
	}
}

func TestSignV4NeverLeaksSecret(t *testing.T) {
	creds := sigV4Credentials{accessKeyID: "AK", secretAccessKey: "super-secret-value", region: "r"}
	req, _ := http.NewRequest(http.MethodGet, "https://b.example/k", nil)
	signV4(req, creds, emptySHA256, time.Now())
	for k, vs := range req.Header {
		for _, v := range vs {
			if strings.Contains(v, creds.secretAccessKey) {
				t.Fatalf("header %s carries the secret", k)
			}
		}
	}
}
