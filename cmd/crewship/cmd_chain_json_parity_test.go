package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// `-f json` must pass the server's answer through, not a subset of it.
//
// This command decodes into its own struct and RE-MARSHALS, so a field the
// struct lacks is silently absent from the output — a JSON consumer sees null
// where the server sent data, and nothing anywhere reports it. chainSummary
// shipped without duration_ms, issues, issue_count, agents and agent_count
// while the server returned all five, and the file already carried a comment
// saying "a field added server-side needs a field here too".
//
// A comment is not a mechanism. This is.
//
// Deliberately a shape test rather than a golden response: the failure is
// always "the server grew a field and this struct did not", and the cheapest
// thing that catches it is comparing the two field sets.
func TestChainSummary_CarriesEveryFieldTheServerSends(t *testing.T) {
	// Captured from GET /api/v1/chains on a live instance. Refresh with:
	//   crewship chain list -f json
	const serverRow = `{
      "origin": "run_x", "started_by_kind": "automation", "started_by_id": "aut_1",
      "started_by_key": "mission.status_change", "started_by": "file a follow-up",
      "triggered_via": "automation", "routine_id": "pln_1", "routine_slug": "followup",
      "runs": 1, "max_chain_depth": 1, "failed_runs": 0, "failed": false,
      "first_activity": "2026-08-10T06:35:58.384000000Z",
      "last_activity": "2026-08-10T06:35:58.397000000Z",
      "duration_ms": 13,
      "issues": [{"id":"m1","identifier":"ENG-8","title":"Follow-up","created":true}],
      "issue_count": 1,
      "agents": [{"id":"a1","slug":"riley","name":"Riley","assignments":2}],
      "agent_count": 1
    }`

	var wire map[string]any
	if err := json.Unmarshal([]byte(serverRow), &wire); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}

	have := map[string]bool{}
	rt := reflect.TypeOf(chainSummary{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		have[strings.Split(tag, ",")[0]] = true
	}

	var dropped []string
	for k := range wire {
		if !have[k] {
			dropped = append(dropped, k)
		}
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		t.Errorf("chainSummary drops %d field(s) the server sends: %s\n"+
			"`-f json` re-marshals this struct, so these reach a consumer as absent while the "+
			"server returned them. Add the field here when one is added server-side.",
			len(dropped), strings.Join(dropped, ", "))
	}
}
