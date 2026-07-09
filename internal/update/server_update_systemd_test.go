package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSystemdService_StopStartArgs(t *testing.T) {
	var calls []string
	svc := &SystemdService{
		Unit: "crewship",
		run: func(_ context.Context, args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			return nil
		},
	}
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "stop crewship" || calls[1] != "start crewship" {
		t.Errorf("systemctl calls = %v, want [stop crewship, start crewship]", calls)
	}
}

func TestParseCrewshipPort(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"present among others", "Environment=FOO=bar CREWSHIP_PORT=9090 BAZ=qux\n", 9090},
		{"only var", "Environment=CREWSHIP_PORT=8443", 8443},
		{"absent", "Environment=FOO=bar\n", 0},
		{"no environment line", "MainPID=1234\n", 0},
		{"empty", "", 0},
		{"unparseable value", "Environment=CREWSHIP_PORT=notaport", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCrewshipPort(tc.in); got != tc.want {
				t.Errorf("parseCrewshipPort(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestHTTPHealthChecker_HealthyImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	check := HTTPHealthChecker(srv.URL+"/healthz", 2*time.Second, 20*time.Millisecond)
	if err := check(context.Background()); err != nil {
		t.Fatalf("expected healthy, got: %v", err)
	}
}

func TestHTTPHealthChecker_BecomesHealthyAfterRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fail the first two probes (server still booting), then serve.
		if atomic.AddInt32(&hits, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	check := HTTPHealthChecker(srv.URL+"/healthz", 2*time.Second, 10*time.Millisecond)
	if err := check(context.Background()); err != nil {
		t.Fatalf("expected eventual health, got: %v", err)
	}
	if atomic.LoadInt32(&hits) < 3 {
		t.Errorf("expected at least 3 probes, got %d", hits)
	}
}

func TestHTTPHealthChecker_TimesOutWhenNeverHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	check := HTTPHealthChecker(srv.URL+"/healthz", 60*time.Millisecond, 10*time.Millisecond)
	err := check(context.Background())
	if err == nil {
		t.Fatal("expected a timeout error when the endpoint never returns 2xx")
	}
	if !strings.Contains(err.Error(), "not healthy") {
		t.Errorf("error should describe the health timeout; got: %v", err)
	}
}
