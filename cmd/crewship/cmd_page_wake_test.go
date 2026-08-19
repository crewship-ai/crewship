package main

// `crewship page create --file` and the sensor half of the document
// (docs/prd/pages.md §5, §4 rule 4).
//
// The CLI's job here is to carry `wake:` and `on_failure:` from the YAML a
// human wrote into the JSON the server validates, and to carry them WITHOUT
// interpreting them: the predicate is parsed server-side, where the panel's
// schema is known and where the refusal has to happen anyway. A CLI that
// dropped them on the floor would produce a page that looks monitored and is
// not — which is the failure the whole feature exists to prevent, arriving
// through the authoring door instead.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pageWakeSpecYAML = `apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201
  slug: flotila-201
spec:
  panels:
    - id: sluzby
      schema: status.v1
      title: Jede to?
      owner: crew/lookout
      producer: script/watch-services.sh
      sla: 60s
      on_failure:
        issue: crew/lookout
      wake:
        - when: any(state == "critical")
          for: 5m
          agent: crew/devops
          writes: incident
    - id: incident
      schema: narrative.v1
      owner: crew/lookout
      producer: agent/scout
      sla: 1h
`

func TestPageCLI_CreateCarriesWakeAndOnFailureIntoTheRequest(t *testing.T) {
	stub := pageStub(t)

	var createdBody []byte
	stub.OnPost("/api/v1/pages", func(_ *http.Request, body []byte) (int, []byte, string) {
		createdBody = append([]byte(nil), body...)
		return http.StatusCreated, body, "application/json"
	})

	specPath := filepath.Join(t.TempDir(), "flotila.page.yaml")
	if err := os.WriteFile(specPath, []byte(pageWakeSpecYAML), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	if _, err := runPageCLI(t, "", "page", "create", "--file", specPath); err != nil {
		t.Fatalf("page create --file: %v", err)
	}

	var sent struct {
		Panels []struct {
			ID   string `json:"id"`
			Wake []struct {
				When   string `json:"when"`
				For    string `json:"for"`
				Agent  string `json:"agent"`
				Writes string `json:"writes"`
			} `json:"wake"`
			OnFailure *struct {
				Issue string `json:"issue"`
			} `json:"on_failure"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(createdBody, &sent); err != nil {
		t.Fatalf("create body is not the expected JSON: %v\n%s", err, string(createdBody))
	}
	if len(sent.Panels) != 2 {
		t.Fatalf("panels sent = %d, want 2", len(sent.Panels))
	}

	sensor := sent.Panels[0]
	if len(sensor.Wake) != 1 {
		t.Fatalf("wake gates sent for %q = %d, want 1 — the CLI dropped the sensor: %s",
			sensor.ID, len(sensor.Wake), string(createdBody))
	}
	g := sensor.Wake[0]
	switch {
	case g.When != `any(state == "critical")`:
		t.Errorf("when = %q, want the predicate as authored — the CLI must not rewrite it", g.When)
	case g.For != "5m":
		t.Errorf("for = %q, want 5m", g.For)
	case g.Agent != "crew/devops":
		t.Errorf("agent = %q, want crew/devops", g.Agent)
	case g.Writes != "incident":
		t.Errorf("writes = %q, want incident", g.Writes)
	}
	if sensor.OnFailure == nil || sensor.OnFailure.Issue != "crew/lookout" {
		t.Errorf("on_failure = %+v, want {issue: crew/lookout} (§4 rule 4)", sensor.OnFailure)
	}

	// A panel that declares neither sends neither, rather than an empty array
	// the server would have to tell apart from an omission.
	if len(sent.Panels[1].Wake) != 0 || sent.Panels[1].OnFailure != nil {
		t.Errorf("a panel with no sensor sent %+v", sent.Panels[1])
	}
}

// A predicate the panel's schema cannot satisfy is the server's refusal to
// make, and the CLI has to surface it rather than swallowing it — this is the
// only way an author finds out before the page is live.
func TestPageCLI_CreateSurfacesAServerSideGateRefusal(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/pages", func(*http.Request, []byte) (int, []byte, string) {
		return http.StatusBadRequest,
			[]byte(`{"error":"panel \"sluzby\": wake gate 1: ` +
				"`when: value > 90` reads a metric.v1 payload, but this panel declares status.v1; " +
				`the gate could never match"}`),
			"application/json"
	})

	specPath := filepath.Join(t.TempDir(), "bad.page.yaml")
	if err := os.WriteFile(specPath, []byte(pageWakeSpecYAML), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	out, err := runPageCLI(t, "", "page", "create", "--file", specPath)
	if err == nil {
		t.Fatalf("page create succeeded against a 400; output: %s", out)
	}
	if !strings.Contains(err.Error()+out, "could never match") {
		t.Errorf("the refusal did not reach the operator: err=%v out=%s", err, out)
	}
}
