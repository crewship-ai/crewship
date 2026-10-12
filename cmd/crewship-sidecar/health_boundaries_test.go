package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/sidecar"
)

func TestHealthProbeUsesHTTPStatusAndExpectedEndpoint(t *testing.T) {
	for _, status := range []int{200, 204, 299, 302, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/health" {
					t.Errorf("unexpected health request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			err := probeHealth(strings.TrimPrefix(server.URL, "http://"))
			if status >= 200 && status < 300 {
				if err != nil {
					t.Fatalf("healthy sidecar rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "unexpected status") {
				t.Fatalf("unhealthy sidecar accepted: %v", err)
			}
		})
	}
}

type healthRoundTripper func(*http.Request) (*http.Response, error)

func (fn healthRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestHealthProbeNormalizesListenAddressesWithoutContactingDefaultPort(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, tc := range []struct{ input, host string }{{"", sidecar.DefaultAddr}, {"  ", sidecar.DefaultAddr}, {":12345", "127.0.0.1:12345"}, {" 127.0.0.1:23456 ", "127.0.0.1:23456"}} {
		http.DefaultTransport = healthRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme != "http" || request.URL.Host != tc.host || request.URL.Path != "/health" {
				t.Errorf("normalized target = %s, wanted host %s", request.URL, tc.host)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("healthy")), Header: make(http.Header)}, nil
		})
		if err := probeHealth(tc.input); err != nil {
			t.Fatalf("listen address %q: %v", tc.input, err)
		}
	}
}

func TestHealthProbeReportsMalformedTargetAndTransportFailure(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	failure := errors.New("fixture transport unavailable")
	http.DefaultTransport = healthRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, failure })
	if err := probeHealth("[invalid"); err == nil || calls != 0 {
		t.Fatalf("malformed address reached transport: %v", err)
	}
	if err := probeHealth("127.0.0.1:12345"); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("transport failure hidden: %v", err)
	}
}

func TestFenceArgumentValidationRefusesBeforeKernelMutation(t *testing.T) {
	for _, tc := range []struct{ uids, dest, detail string }{{"not-a-uid", "", "invalid uid"}, {"1002", "not-a-destination", ""}} {
		var stdout, stderr bytes.Buffer
		if status := runFence(true, tc.uids, tc.dest, &stdout, &stderr); status != fenceExitError {
			t.Fatalf("invalid fence accepted: %d", status)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "fence:") || !strings.Contains(stderr.String(), tc.detail) {
			t.Fatalf("fence validation diagnostic: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	}
}

func TestFenceDestinationsPreserveEndpointProtocolAndIgnoreBlankEntries(t *testing.T) {
	destinations, err := parseFenceDests(" , 10.231.0.3:6379/tcp, ,10.231.0.4:53/udp, ")
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 2 || destinations[0].Addr.String() != "10.231.0.3" || destinations[0].Port != 6379 || destinations[0].Proto != "tcp" || destinations[1].Addr.String() != "10.231.0.4" || destinations[1].Port != 53 || destinations[1].Proto != "udp" {
		t.Fatalf("fence endpoint parsing: %#v", destinations)
	}
}
