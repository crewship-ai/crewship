package gatekeeper

import "testing"

// The prompt is the question the judge was asked. Truncating it to 2000
// characters made two things impossible at once, and both were discovered the
// hard way on 2026-08-03.
//
// The audit trail could not answer "what was it asked?" — the stored prompt for
// a production-credential decision was a fragment ending mid-sentence.
//
// And `keeper eval` replays that column. Every number the harness ever produced
// measured how a model behaves on a mutilated prompt: two models as different
// as a 9B local one and a hosted frontier one returned the SAME 0.625 agreement
// to three decimals, because both fail closed when the criteria and the schema
// instruction are cut off the end. Median corpus prompt length was 2014 —
// exactly 2000 plus the marker — in BOTH halves of a split by prompt size,
// which is what a clamp looks like when you plot it.
//
// The response stays capped. It is a model's prose, it is bounded by
// replayMaxTokens anyway, and nothing replays it.

func TestPromptIsStoredWhole(t *testing.T) {
	long := make([]byte, maxAuditText*3)
	for i := range long {
		long[i] = 'x'
	}
	if got := truncatePromptForAudit(string(long)); len(got) != len(long) {
		t.Errorf("prompt stored at %d of %d bytes — an audit that cannot show what "+
			"the judge was asked is not an audit, and the eval replays this column",
			len(got), len(long))
	}
}

// The response cap stays: it bounds a model's free-form prose, and no harness
// replays it.
func TestResponseIsStillCapped(t *testing.T) {
	long := make([]byte, maxAuditText*3)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateForAudit(string(long))
	if len(got) >= len(long) {
		t.Errorf("response grew to %d bytes; the cap is there to bound model prose", len(got))
	}
}
