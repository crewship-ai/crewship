package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestAuthorizationAuditProjectionBeforePreviewAndObserver(t *testing.T) {
	for _, tc := range []struct {
		typ journal.EntryType
		key string
	}{
		{journal.EntryMemberCapabilitiesChanged, "capabilities"},
		{journal.EntryPageGrantAdded, "grant"},
		{journal.EntryPageGrantRemoved, "previous_grant"},
		{"page.folder_acl_changed", "acl"},
	} {
		t.Run(string(tc.typ), func(t *testing.T) {
			e := journal.Entry{WorkspaceID: "ws_1", Type: tc.typ, Payload: map[string]any{"action": "changed", tc.key: "private-audit-value"}}
			before, _ := json.Marshal(e)
			m := Matcher{PayloadEquals: map[string]any{tc.key: "private-audit-value"}}
			if !m.Matches(e) {
				t.Fatal("raw canonical matcher calibration failed")
			}
			result := Preview(m, string(tc.typ), []journal.Entry{e})
			if result.Scanned != 1 || result.Matched != 0 || !result.TopRejection.KeyAbsent || strings.Contains(result.TopRejection.Detail, "private-audit-value") {
				t.Fatalf("private preview oracle: %+v", result)
			}
			result = Preview(Matcher{}, string(tc.typ), []journal.Entry{e})
			if len(result.Samples) != 1 {
				t.Fatal("public preview event missing")
			}
			if _, ok := result.Samples[0].Payload[tc.key]; ok {
				t.Fatal("preview sample contains private field")
			}
			enq := &recordingEnqueuer{}
			registry := NewRegistry(nil, enq, Options{})
			denied := rule("private-match", "ws_1", string(tc.typ))
			denied.Matcher = m
			public := rule("public-match", "ws_1", string(tc.typ))
			public.Action.Inputs = map[string]any{"private": "{{ event.payload." + tc.key + " }}", "public": "{{ event.payload.action }}"}
			registry.Load([]Resolved{denied, public})
			registry.Observer([]journal.Entry{e})
			registry.Flush(context.Background())
			if enq.n() != 1 {
				t.Fatalf("observer private matcher fired or public event lost: %d", enq.n())
			}
			var inputs map[string]any
			if err := json.Unmarshal([]byte(enq.at(0).InputsJSON), &inputs); err != nil {
				t.Fatal(err)
			}
			if inputs["public"] != "changed" || strings.Contains(enq.at(0).InputsJSON, "private-audit-value") {
				t.Fatalf("observer rendering leaked or lost public field: %v", inputs)
			}
			after, _ := json.Marshal(e)
			if string(before) != string(after) {
				t.Fatal("automation mutated canonical entry")
			}
		})
	}
}
