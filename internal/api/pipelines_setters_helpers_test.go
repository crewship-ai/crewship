package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ---------------------------------------------------------------------------
// pipelines.go — setter/getter wiring (SetSaveTokenSecret, SetJournal,
// SetWaitpointStore, SetWSBroadcaster + Runner/Emitter/RunRegistry
// accessors) and the small pure helpers (definitionHashHex,
// parseSmallInt, truncateErrorForList).
// ---------------------------------------------------------------------------

// pipelineAgentRunnerStub satisfies pipeline.AgentRunner — single
// method, lets the Runner getter test confirm round-trip identity.
type pipelineAgentRunnerStub struct{}

func (pipelineAgentRunnerStub) RunStep(_ context.Context, _ pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	return pipeline.AgentStepResult{}, nil
}

// pipelineEmitterStub satisfies pipeline.Emitter (single Emit method
// taking journal.Entry).
type pipelineEmitterStub struct{}

func (pipelineEmitterStub) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", nil
}

// --- SetSaveTokenSecret ---

func TestSetSaveTokenSecret_StoresSecret(t *testing.T) {
	h := &PipelineHandler{}
	secret := []byte("my-shared-secret-for-test")
	h.SetSaveTokenSecret(secret)
	if string(h.saveTokenSecret) != string(secret) {
		t.Errorf("saveTokenSecret not stored; got %q", h.saveTokenSecret)
	}
}

func TestSetSaveTokenSecret_NilClearsSecret(t *testing.T) {
	h := &PipelineHandler{saveTokenSecret: []byte("prior")}
	h.SetSaveTokenSecret(nil)
	if h.saveTokenSecret != nil {
		t.Errorf("nil secret should clear field; got %q", h.saveTokenSecret)
	}
}

// --- SetWaitpointStore ---

func TestSetWaitpointStore_NilLandsAsNil(t *testing.T) {
	// Pin the field's nil contract — newExecutor()'s `if h.waitpoints
	// != nil` check distinguishes wired from unwired; a regression that
	// silently swaps nil for a noop would break that detection.
	h := &PipelineHandler{}
	h.SetWaitpointStore(nil)
	if h.waitpoints != nil {
		t.Error("nil waitpoints should leave field nil")
	}
}

// --- SetWSBroadcaster ---

func TestSetWSBroadcaster_NilLandsAsNil(t *testing.T) {
	h := &PipelineHandler{}
	h.SetWSBroadcaster(nil)
	if h.ws != nil {
		t.Error("nil ws should leave field nil")
	}
}

// --- SetJournal ---

func TestSetJournal_StoresAndExposesViaGetter(t *testing.T) {
	h := &PipelineHandler{}
	if h.Emitter() != nil {
		t.Error("Emitter on fresh handler = non-nil, want nil")
	}
	fake := pipelineEmitterStub{}
	h.SetJournal(fake)
	if h.Emitter() != fake {
		t.Error("Emitter did not return SetJournal's value")
	}
}

func TestSetJournal_NilLandsAsNil(t *testing.T) {
	// Unlike the agent handler's SetJournal which swaps nil for a
	// noopEmitter, pipelines.SetJournal stores whatever is passed.
	// pipeline.NewExecutor's ensureEmitter handles the nil-swap
	// downstream. Pin the direct-store behavior so a refactor that
	// introduces a nil-swap here doesn't change downstream identity.
	h := &PipelineHandler{}
	h.SetJournal(nil)
	if h.emitter != nil {
		t.Errorf("emitter = %v, want nil (downstream ensureEmitter handles nil-swap)", h.emitter)
	}
}

// --- Runner getter ---

func TestRunner_NilBeforeSet_NonNilAfter(t *testing.T) {
	h := &PipelineHandler{}
	if h.Runner() != nil {
		t.Error("Runner on fresh handler = non-nil, want nil")
	}
	fake := pipelineAgentRunnerStub{}
	h.SetRunner(fake)
	if h.Runner() != fake {
		t.Error("Runner did not return the value set via SetRunner")
	}
}

// --- RunRegistry getter ---

func TestRunRegistry_NilBeforeSet_NonNilAfter(t *testing.T) {
	h := &PipelineHandler{}
	if h.RunRegistry() != nil {
		t.Error("RunRegistry on fresh handler = non-nil, want nil")
	}
	reg := pipeline.NewRunRegistry()
	h.SetRunRegistry(reg)
	if h.RunRegistry() != reg {
		t.Error("RunRegistry getter did not return SetRunRegistry's value")
	}
}

// --- definitionHashHex ---

func TestDefinitionHashHex_DelegatesToPipelinePackage(t *testing.T) {
	in := []byte(`{"name":"x","steps":[]}`)
	got := definitionHashHex(in)
	// Source comment: must equal pipeline.DefinitionHash output for
	// the save_token signer/store agreement.
	sum := sha256.Sum256(in)
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("got %q, want %q (sha256 hex of input)", got, want)
	}
	// Sanity: deterministic — same input → same output.
	if got2 := definitionHashHex(in); got2 != got {
		t.Errorf("non-deterministic: %q vs %q", got, got2)
	}
	// Different input → different hash.
	if definitionHashHex([]byte("different")) == got {
		t.Error("different input collided with prior hash")
	}
}

// --- parseSmallInt ---

func TestParseSmallInt_Cases(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   int
		hasErr bool
	}{
		{"empty", "", 0, true},
		{"zero", "0", 0, false},
		{"single-digit", "5", 5, false},
		{"three-digit", "123", 123, false},
		{"max-allowed", "9999", 9999, false},
		{"one-over-cap", "10000", 0, true},
		{"way-over-cap", "1234567", 0, true},
		{"non-digit-letter", "12a", 0, true},
		{"leading-space", " 1", 0, true},
		{"negative-sign", "-1", 0, true},
		{"plus-sign", "+1", 0, true},
		{"float-like", "1.5", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSmallInt(tc.in)
			if tc.hasErr {
				if err == nil {
					t.Errorf("parseSmallInt(%q) err = nil, want non-nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Errorf("parseSmallInt(%q) err = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseSmallInt(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// --- truncateErrorForList ---

func TestTruncateErrorForList_Cases(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"short", "boom", "boom"},
		{"newline-stops-at-first-line", "first line\nstack frame\n  more", "first line"},
		{"crlf-also-stops", "first\r\nsecond", "first\r"}, // source uses IndexByte('\n') only
		{"exactly-200", strings.Repeat("a", 200), strings.Repeat("a", 200)},
		{"201-truncates", strings.Repeat("a", 201), strings.Repeat("a", 200) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateErrorForList(tc.in)
			if got != tc.want {
				t.Errorf("truncateErrorForList(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateErrorForList_MultibyteSafe(t *testing.T) {
	// Source comment: "UTF-8 safe slice — walk back to a rune boundary
	// so we don't emit invalid bytes." Build a string of 199 ASCII 'a'
	// + multibyte runes so the cap (200) lands mid-multibyte; walking
	// back should drop the partial rune rather than produce invalid UTF-8.
	in := strings.Repeat("a", 199) + "ěššš"
	got := truncateErrorForList(in)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("output missing ellipsis: %q", got)
	}
	// Must be valid UTF-8 — no half-rune bytes leaked.
	for i := 0; i < len(got); {
		_, size := decodeRune(got[i:])
		if size == 0 {
			t.Errorf("invalid UTF-8 at byte %d in %q", i, got)
			return
		}
		i += size
	}
}

// decodeRune is a minimal valid-utf8 walker — returns size=0 on invalid
// sequences. Avoids importing unicode/utf8 for one assertion.
func decodeRune(s string) (rune, int) {
	if len(s) == 0 {
		return 0, 0
	}
	b := s[0]
	switch {
	case b < 0x80:
		return rune(b), 1
	case b < 0xC2:
		return 0, 0 // continuation byte at start
	case b < 0xE0:
		if len(s) < 2 || s[1]&0xC0 != 0x80 {
			return 0, 0
		}
		return rune(b&0x1F)<<6 | rune(s[1]&0x3F), 2
	case b < 0xF0:
		if len(s) < 3 || s[1]&0xC0 != 0x80 || s[2]&0xC0 != 0x80 {
			return 0, 0
		}
		return rune(b&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	case b < 0xF5:
		if len(s) < 4 || s[1]&0xC0 != 0x80 || s[2]&0xC0 != 0x80 || s[3]&0xC0 != 0x80 {
			return 0, 0
		}
		return rune(b&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F), 4
	}
	return 0, 0
}
