package pages

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStoredPagePayloadsRemainUsableByWakeEvaluation(t *testing.T) {
	for _, tc := range []struct {
		schema PanelSchema
		raw    string
	}{
		{SchemaMetric, `{"value":0}`},
		{SchemaSeries, `{"points":[]}`},
		{SchemaStatus, `{"items":[{"name":"api","state":"warning"},{"name":"db","state":"critical"},{"name":"worker","state":"ok"}]}`},
		{SchemaTable, `{"columns":[{"key":"n","label":"Count","type":"number"}],"rows":[{"n":9007199254740993}]}`},
		{SchemaNarrative, `{"blocks":[{"kind":"paragraph","text":"Healthy"}]}`},
	} {
		t.Run(string(tc.schema), func(t *testing.T) {
			payload, ok := DecodeStoredPayload(tc.schema, []byte(tc.raw))
			if !ok || payload.Schema() != tc.schema {
				t.Fatalf("stored payload unavailable: %#v %v", payload, ok)
			}
			if status, ok := payload.(*StatusPayload); ok {
				if worst, present := status.Worst(); !present || worst != StatusCritical {
					t.Fatalf("lost critical status: %s %v", worst, present)
				}
			}
			if table, ok := payload.(*TablePayload); ok {
				cell := table.Rows[0]["n"]
				if string(cell.Raw()) != "9007199254740993" || cell.IsNoData() {
					t.Fatalf("lost integer precision: %s", cell.Raw())
				}
				encoded, err := json.Marshal(cell)
				if err != nil || string(encoded) != "9007199254740993" {
					t.Fatalf("cell roundtrip: %s %v", encoded, err)
				}
			}
			if payload, ok := DecodeStoredPayload(tc.schema, []byte(`{"broken":`)); ok || payload != nil {
				t.Fatalf("corrupt row became evidence: %#v %v", payload, ok)
			}
		})
	}
	if payload, ok := DecodeStoredPayload("future.v9", []byte(`{}`)); ok || payload != nil {
		t.Fatalf("unknown schema became evidence: %#v %v", payload, ok)
	}
	for _, status := range []*StatusPayload{nil, {}, {Items: []StatusItem{}}} {
		if state, present := status.Worst(); present || state != "" {
			t.Fatalf("empty row claims measured health: %s %v", state, present)
		}
	}
	for _, state := range []StatusState{StatusOK, StatusWarning} {
		p := &StatusPayload{Items: []StatusItem{{Name: "api", State: state}}}
		if got, ok := p.Worst(); !ok || got != state {
			t.Fatalf("state changed: %s %v", got, ok)
		}
	}
	var cell Cell
	if encoded, err := json.Marshal(cell); err != nil || string(encoded) != "null" || !cell.IsNoData() {
		t.Fatalf("absent cell: %s %v", encoded, err)
	}
}

func TestPagePanelReadPermissionDoesNotExpandWithPageAccess(t *testing.T) {
	for _, role := range []string{"OWNER", "ADMIN", "MEMBER", "VIEWER", "", "owner"} {
		for _, member := range []bool{false, true} {
			want := member || role == "OWNER" || role == "ADMIN"
			if got := CanSeePanel(role, member); got != want {
				t.Fatalf("role %q crew member %v: %v", role, member, got)
			}
		}
	}
}

func TestEmbedEnvironmentPolicyAndBrowserCSPAgree(t *testing.T) {
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.example.com")
	t.Setenv("CREWSHIP_PAGES_EMBED_SOURCES", "status=https://status.example.com/view,metrics=https://metrics.example.com/")
	policy, err := EmbedPolicyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if policy.Len() != 2 {
		t.Fatalf("source count %d", policy.Len())
	}
	sources := policy.Sources()
	if len(sources) != 2 || sources[0].Name != "status" || sources[1].Name != "metrics" {
		t.Fatalf("source order: %#v", sources)
	}
	sources[0].URL = "https://untrusted.example/"
	if source, ok := policy.Lookup("status"); !ok || source.URL != "https://status.example.com/view" {
		t.Fatal("caller mutated installed allow-list")
	}
	restore := SetEmbedPolicy(policy)
	defer restore()
	if got := FrameSrcDirective(); got != "frame-src https://metrics.example.com https://status.example.com" {
		t.Fatalf("unexpected CSP %q", got)
	}
	if !reflect.DeepEqual(CurrentEmbedPolicy().Sources(), policy.Sources()) {
		t.Fatal("installed policy differs")
	}
	t.Setenv("CREWSHIP_PAGES_EMBED_SOURCES", "bad=https://crewship.example.com/internal")
	if _, err := EmbedPolicyFromEnv(); err == nil {
		t.Fatal("same-origin source accepted")
	}
	restoreEmpty := SetEmbedPolicy(EmbedPolicy{})
	defer restoreEmpty()
	if FrameSrcDirective() != "frame-src 'none'" {
		t.Fatal("empty policy permits frames")
	}
}
