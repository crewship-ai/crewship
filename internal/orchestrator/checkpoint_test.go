package orchestrator

import "testing"

func TestParseCheckpoint_WellFormedBlock(t *testing.T) {
	text := "I did the work.\n\n---CHECKPOINT---\n" +
		"done: implemented the parser and its tests\n" +
		"plan: wire it into dispatch next\n" +
		"facts: the endpoint is GET /issue/{id}/comments\n" +
		"blockers: none\n" +
		"next_step: add the acceptance test\n" +
		"confidence: high\n" +
		"---END CHECKPOINT---\n"
	cp := ParseCheckpoint(text)
	if !cp.Parsed {
		t.Fatalf("expected Parsed=true, got %+v", cp)
	}
	if cp.Done != "implemented the parser and its tests" {
		t.Errorf("Done = %q", cp.Done)
	}
	if cp.NextStep != "add the acceptance test" {
		t.Errorf("NextStep = %q", cp.NextStep)
	}
	if cp.Confidence != "high" {
		t.Errorf("Confidence = %q", cp.Confidence)
	}
}

func TestParseCheckpoint_NoBlock_ReportsUnparsed(t *testing.T) {
	cp := ParseCheckpoint("I did some work but forgot the checkpoint block.")
	if cp.Parsed {
		t.Fatalf("expected Parsed=false, got %+v", cp)
	}
	if cp.Done != "" || cp.NextStep != "" {
		t.Errorf("expected empty fields on an unparsed checkpoint, got %+v", cp)
	}
}

func TestParseCheckpoint_PartialBlock_MissingDoneAndNextStep_IsUnparsed(t *testing.T) {
	text := "---CHECKPOINT---\nconfidence: low\n---END CHECKPOINT---"
	cp := ParseCheckpoint(text)
	if cp.Parsed {
		t.Fatalf("a block naming neither done nor next_step must not count as parsed: %+v", cp)
	}
}

func TestParseCheckpoint_MultilineField(t *testing.T) {
	text := "---CHECKPOINT---\n" +
		"done: shipped the feature\n" +
		"facts:\n- id A\n- id B\n" +
		"next_step: done\n" +
		"---END CHECKPOINT---"
	cp := ParseCheckpoint(text)
	if !cp.Parsed {
		t.Fatalf("expected Parsed=true, got %+v", cp)
	}
	if cp.Facts != "- id A\n- id B" {
		t.Errorf("Facts = %q, want multi-line list", cp.Facts)
	}
}

func TestParseCheckpoint_CaseInsensitivePrefix(t *testing.T) {
	text := "---CHECKPOINT---\nDone: finished it\nNext_Step: ship it\n---END CHECKPOINT---"
	cp := ParseCheckpoint(text)
	if !cp.Parsed || cp.Done != "finished it" || cp.NextStep != "ship it" {
		t.Fatalf("expected case-insensitive prefixes to parse, got %+v", cp)
	}
}

func TestParseCheckpoint_LastBlockWins(t *testing.T) {
	text := "---CHECKPOINT---\ndone: draft one\n---END CHECKPOINT---\n" +
		"more output\n" +
		"---CHECKPOINT---\ndone: final answer\nnext_step: none\n---END CHECKPOINT---"
	cp := ParseCheckpoint(text)
	if !cp.Parsed || cp.Done != "final answer" {
		t.Fatalf("expected the LAST checkpoint block to win, got %+v", cp)
	}
}
