package backup

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestRekeyForkedCapabilityTokensDigestOnlyRows(t *testing.T) {
	sourceDigest := pipeline.HashCapabilityToken("source-secret")
	dump := &DBDump{Tables: map[string][]map[string]any{
		"port_exposures": {
			{"id": "exposure-digest", "token": "", "token_hash": sourceDigest, "status": "ACTIVE"},
			{"id": "exposure-empty", "token": "", "token_hash": "", "status": "ACTIVE"},
		},
		"pipeline_webhooks": {
			{"id": "webhook-digest", "token": "", "token_hash": sourceDigest, "enabled": int64(1)},
			{"id": "webhook-empty", "token": "", "token_hash": "", "enabled": int64(1)},
		},
	}}

	counts, err := rekeyForkedCapabilityTokens(dump)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"port_exposures", "pipeline_webhooks"} {
		if counts[table] != 1 {
			t.Errorf("%s re-keyed %d rows, want 1", table, counts[table])
		}
		row := dump.Tables[table][0]
		if got, _ := row["token_hash"].(string); got == sourceDigest || !pipeline.IsCapabilityTokenDigest(got) {
			t.Errorf("%s kept source digest or wrote invalid digest: %q", table, got)
		}
		if got, _ := row["token"].(string); !strings.HasPrefix(got, "redacted:") {
			t.Errorf("%s did not redact digest-only row: %q", table, got)
		}
		if empty := dump.Tables[table][1]; empty["token"] != "" || empty["token_hash"] != "" {
			t.Errorf("%s invented a capability for an empty row: %+v", table, empty)
		}
	}
	if row := dump.Tables["port_exposures"][0]; row["status"] != "REVOKED" || row["revoked_at"] == nil {
		t.Errorf("digest-only exposure remained live: %+v", row)
	}
	if row := dump.Tables["pipeline_webhooks"][0]; row["enabled"] != int64(0) {
		t.Errorf("digest-only webhook remained enabled: %+v", row)
	}
}
