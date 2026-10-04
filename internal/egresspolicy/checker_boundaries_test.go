package egresspolicy

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/egressallow"
)

func TestResolvedAllowlistMatchesDatabasePolicyAndMode(t *testing.T) {
	db := openCrewsTestDB(t)
	seedCrew(t, db, "crew", "restricted", `["partner.example"]`)
	checks := []HostChecker{DBChecker(db, "crew"), AllowlistChecker(egressallow.NewDomainAllowlist([]string{"partner.example"}), false)}
	for _, check := range checks {
		if err := check(t.Context(), "PARTNER.EXAMPLE:443"); err != nil {
			t.Fatal(err)
		}
		if err := check(t.Context(), "foreign.example"); err == nil {
			t.Fatal("foreign host passed restricted policy")
		}
	}
	for _, check := range []HostChecker{AllowlistChecker(nil, false), AllowlistChecker(egressallow.NewDomainAllowlist(nil), true), NoopChecker()} {
		if err := check(t.Context(), "foreign.example"); err != nil {
			t.Fatalf("unrestricted policy blocked: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checks[0](t.Context(), "partner.example"); err == nil {
		t.Fatal("unreadable database policy allowed request")
	}
}

func TestRedirectDefaultsAndAdditionalGateRemainFailClosed(t *testing.T) {
	client := Client(nil, Options{})
	if client.Transport == nil || client.Timeout != 30*time.Second {
		t.Fatal("default client lost bounded safe transport")
	}
	req, err := http.NewRequestWithContext(t.Context(), "GET", "https://partner.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(req, nil); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://partner.example/", "https://127.0.0.1/", "https://169.254.169.254/"} {
		req, err := http.NewRequestWithContext(t.Context(), "GET", target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.CheckRedirect(req, nil); err == nil {
			t.Fatalf("unsafe redirect allowed: %s", target)
		}
	}
	blocked := errors.New("routine egress target denied")
	calls := 0
	client = Client(nil, Options{Timeout: time.Second, AllowPrivate: true, ExtraHop: func(r *http.Request) error { calls++; return blocked }})
	if client.Timeout != time.Second {
		t.Fatal("explicit timeout ignored")
	}
	if err := client.CheckRedirect(req, nil); !errors.Is(err, blocked) || calls != 1 {
		t.Fatalf("extra gate not enforced: %v (%d calls)", err, calls)
	}
}
