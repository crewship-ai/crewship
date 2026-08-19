package main

// `crewship page create --file` and `refresh:` (docs/prd/pages.md §12 v1.1).
//
// The CLI's job is the same one it has for `wake:` — carry the field from the
// YAML a human wrote into the JSON the server validates, and carry it WITHOUT
// interpreting it. What makes dropping it worse than dropping a tab is what is
// on the other end: the field compiles to an `automations` row, so a CLI that
// lost it would produce a page that goes on LOOKING like it refreshes and
// silently stops running the routine. `page update --file` is the sharper case
// — the server replaces the panel set with what it is sent, so "not mentioned"
// reads as "removed".

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pageRefreshSpecYAML is the PRD's §6 worked example, at the v1.1 feature level.
const pageRefreshSpecYAML = `apiVersion: crewship/v1
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
      sla: 30s
      span: 8
      wake:
        - when: any(state == "critical")
          agent: crew/devops
          writes: incident
    - id: incident
      schema: narrative.v1
      owner: crew/devops
      producer: routine/incident-rozbor
      refresh: on:wake
      sla: 1h
      span: 12
`

func TestPageCLI_CreateCarriesRefreshIntoTheRequest(t *testing.T) {
	stub := pageStub(t)

	var createdBody []byte
	stub.OnPost("/api/v1/pages", func(_ *http.Request, body []byte) (int, []byte, string) {
		createdBody = append([]byte(nil), body...)
		return http.StatusCreated, body, "application/json"
	})

	specPath := filepath.Join(t.TempDir(), "flotila.page.yaml")
	if err := os.WriteFile(specPath, []byte(pageRefreshSpecYAML), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	if _, err := runPageCLI(t, "", "page", "create", "--file", specPath); err != nil {
		t.Fatalf("page create --file: %v", err)
	}

	var sent struct {
		Panels []struct {
			ID      string `json:"id"`
			Refresh string `json:"refresh"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(createdBody, &sent); err != nil {
		t.Fatalf("create body is not the expected JSON: %v\n%s", err, string(createdBody))
	}
	if len(sent.Panels) != 2 {
		t.Fatalf("panels sent = %d, want 2", len(sent.Panels))
	}
	if sent.Panels[1].Refresh != "on:wake" {
		t.Errorf("refresh sent for %q = %q, want on:wake — the CLI dropped the trigger, and the "+
			"next `page update --file` would delete its automations row: %s",
			sent.Panels[1].ID, sent.Panels[1].Refresh, string(createdBody))
	}
	// A panel that declares none sends none, rather than an empty string the
	// server would have to tell apart from an omission.
	if sent.Panels[0].Refresh != "" {
		t.Errorf("a panel declaring no refresh sent %q", sent.Panels[0].Refresh)
	}
}

// The closed set is refused CLIENT-side, by ParseDocument, before anything is
// sent — the same door that already refuses an unknown icon. An author who
// mistyped it finds out from `crewship page create` rather than from a routine
// that never runs.
func TestPageCLI_CreateRefusesARefreshOutsideTheClosedSet(t *testing.T) {
	pageStub(t)

	bad := strings.Replace(pageRefreshSpecYAML, "refresh: on:wake", "refresh: on:push", 1)
	specPath := filepath.Join(t.TempDir(), "bad.page.yaml")
	if err := os.WriteFile(specPath, []byte(bad), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	out, err := runPageCLI(t, "", "page", "create", "--file", specPath)
	if err == nil {
		t.Fatalf("page create accepted `refresh: on:push`; output: %s", out)
	}
	if !strings.Contains(err.Error()+out, "on:panels-changed") {
		t.Errorf("the refusal does not name the vocabulary: err=%v out=%s", err, out)
	}
}

// A `refresh:` whose producer the server cannot run is refused at the same
// door, with the reason rather than a bare "invalid".
func TestPageCLI_CreateRefusesARefreshOnAScriptProducer(t *testing.T) {
	pageStub(t)

	bad := strings.Replace(pageRefreshSpecYAML,
		"producer: routine/incident-rozbor", "producer: script/rozbor.sh", 1)
	specPath := filepath.Join(t.TempDir(), "script.page.yaml")
	if err := os.WriteFile(specPath, []byte(bad), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	out, err := runPageCLI(t, "", "page", "create", "--file", specPath)
	if err == nil {
		t.Fatalf("page create accepted a refresh on a script producer; output: %s", out)
	}
	if !strings.Contains(err.Error()+out, "routine/") {
		t.Errorf("the refusal does not say what a refreshable producer is: err=%v out=%s", err, out)
	}
}
