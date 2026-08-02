package main

// CLI-side consequences of the container TTL default (#1662).
//
// `crewship crew get ops` reporting "TTL: Never stop" is how the missing
// default was found in the first place. Once NULL means "the server default",
// that same line becomes a lie in the other direction — it would promise a
// container will never be stopped four hours before the reaper stops it.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCrewGetRunE_NullTTLRendersServerDefaultNotNeverStop(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli4, "name": "Ops", "slug": "ops",
		"container_memory_mb": 4096, "container_cpus": 2.0,
		"created_at": "2026-05-01T00:00:00Z",
	}))

	c := covFreshCmd(crewGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Never stop") {
		t.Errorf("an unconfigured crew still reports 'Never stop': %q", out)
	}
	if !strings.Contains(out, "Server default") {
		t.Errorf("unconfigured TTL should name the server default: %q", out)
	}
}

func TestCrewGetRunE_ExplicitZeroTTLStillRendersNeverStop(t *testing.T) {
	// The CLI must not collapse "unset" and "explicitly none" the way the
	// server used to — they are now different states with different outcomes.
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli4, "name": "Ops", "slug": "ops",
		"container_memory_mb": 4096, "container_cpus": 2.0,
		"container_ttl_hours": 0,
		"created_at":          "2026-05-01T00:00:00Z",
	}))

	c := covFreshCmd(crewGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Never stop") {
		t.Errorf("an explicit TTL of 0 should render 'Never stop': %q", out)
	}
}

func TestCrewGetRunE_TTLAndUnenforcedEgressBothRenderOnOneScreen(t *testing.T) {
	// #1662 and #1648 both write into this one detail view, and they were
	// resolved against each other in a rebase. The mode string and the TTL
	// string are computed on adjacent lines; a merge that keeps one and drops
	// the other compiles fine. `networkModeDisplay` had a unit test of its
	// own, but nothing covered the "Why Not Enforced" row it feeds, so
	// deleting that row from the rendered pairs was invisible.
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli4, "name": "Ops", "slug": "ops",
		"container_memory_mb": 4096, "container_cpus": 2.0,
		"container_ttl_hours":   7,
		"network_mode":          "restricted",
		"network_mode_enforced": false,
		"network_mode_unenforced_reason": "egress is enforced by the in-container " +
			"crewship-sidecar proxy, whose binary this provider does not mount",
		"created_at": "2026-05-01T00:00:00Z",
	}))

	c := covFreshCmd(crewGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		"7 hours",                   // #1662
		"restricted (NOT ENFORCED)", // #1648
		"Why Not Enforced",          // #1648's reason row
		"crewship-sidecar proxy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("crew get output missing %q: %q", want, out)
		}
	}
}

func TestCrewGetRunE_EnforcedCrewOmitsTheWhyNotEnforcedRow(t *testing.T) {
	// The reason row must not appear for a crew whose fence is real —
	// otherwise the row stops meaning anything.
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli4, "name": "Ops", "slug": "ops",
		"container_memory_mb": 4096, "container_cpus": 2.0,
		"container_ttl_hours":   7,
		"network_mode":          "restricted",
		"network_mode_enforced": true,
		"created_at":            "2026-05-01T00:00:00Z",
	}))

	c := covFreshCmd(crewGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Why Not Enforced") {
		t.Errorf("enforced crew rendered a Why Not Enforced row: %q", out)
	}
	if strings.Contains(out, "NOT ENFORCED") {
		t.Errorf("enforced crew rendered a NOT ENFORCED mode: %q", out)
	}
	if !strings.Contains(out, "7 hours") {
		t.Errorf("TTL missing from an enforced crew's detail: %q", out)
	}
}

func TestCrewCreateRunE_ExplicitZeroTTLIsForwarded(t *testing.T) {
	// Create dropped the field on `ttl > 0`, so `--ttl 0` sent nothing and the
	// server stored NULL. That was harmless while NULL meant never-stop; now
	// it means the server default, so an operator asking for never-stop would
	// silently get a four-hour auto-stop.
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews", clitest.JSONResponse(201, map[string]string{
		"id": covCrewIDCli4, "slug": "ops",
	}))

	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Ops", "ttl": "0"})
	if _, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) }); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/crews")
	if len(calls) != 1 {
		t.Fatalf("POST /crews calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, present := body["container_ttl_hours"]
	if !present {
		t.Fatal("--ttl 0 sent no container_ttl_hours; the server then applies its default instead of never-stop")
	}
	if v != float64(0) {
		t.Errorf("container_ttl_hours = %v, want 0", v)
	}
}

func TestCrewCreateRunE_OmittedTTLSendsNothing(t *testing.T) {
	// The flip side: an operator who says nothing must get the server
	// default, so the field must stay absent from the body.
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews", clitest.JSONResponse(201, map[string]string{
		"id": covCrewIDCli4, "slug": "ops",
	}))

	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Ops"})
	if _, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) }); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(stub.CallsFor("POST", "/api/v1/crews")[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := body["container_ttl_hours"]; present {
		t.Errorf("an omitted --ttl sent container_ttl_hours = %v; want the field absent", body["container_ttl_hours"])
	}
}
