//go:build linux

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCommandPrivateJSONProfilesPreserveLegacyAndStrictBounds(t *testing.T) {
	// The first failing actual checkpoint had 35089 structural delimiters.
	wide := []byte("[" + strings.Repeat("0,", 35088) + "0]")
	if _, err := mooring.CanonicalJSON(wide); err == nil {
		t.Fatal("legacy structure guard widened")
	}
	if _, err := commandCheckpointJSON(wide); err != nil {
		t.Fatal("C1 checkpoint profile still applies legacy structure guard")
	}
	if mooring.MaxCanonicalStructure != 32768 || mooring.MaxCanonicalBytes != 1<<20 || mooring.MaxCanonicalDepth != 64 {
		t.Fatal("legacy profile changed")
	}

	for name, raw := range map[string][]byte{"duplicate": []byte(`{"a":1,"a":2}`), "escaped_duplicate": []byte(`{"a":1,"\u0061":2}`), "nested_duplicate": []byte(`{"a":{"x":1,"x":2}}`), "utf8": {0xff}, "syntax": []byte(`{"a":`), "empty": {}, "depth": []byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65))} {
		t.Run(name, func(t *testing.T) {
			if _, err := commandCheckpointJSON(raw); err == nil {
				t.Fatal("invalid checkpoint JSON accepted")
			}
			if _, err := commandPrivateFrameJSON(raw); err == nil {
				t.Fatal("invalid private frame accepted")
			}
		})
	}
	depth := []byte(strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64))
	if _, e := commandCheckpointJSON(depth); e != nil {
		t.Fatal("depth64 refused")
	}
	atStructure := []byte("[" + strings.Repeat("0,", commandNativeJSONStructureLimit-1) + "0]")
	if _, err := commandCheckpointJSON(atStructure); err != nil {
		t.Fatal("exact structure bound refused")
	}
	aboveStructure := append([]byte("[0,"), atStructure[1:]...)
	if _, err := commandCheckpointJSON(aboveStructure); err == nil {
		t.Fatal("structure overflow accepted")
	}
	atBytes := []byte(`"` + strings.Repeat("x", commandCheckpointLimit-2) + `"`)
	if _, err := commandCheckpointJSON(atBytes); err != nil {
		t.Fatal("exact checkpoint byte bound refused")
	}
	if _, err := commandCheckpointJSON(append(atBytes, ' ')); err == nil {
		t.Fatal("checkpoint byte overflow accepted")
	}
}

func TestCommandPrivateFrameQuotedOverheadAndExactByteBound(t *testing.T) {
	// Same actual decoder used by native replies; no HTTP/public command widening.
	quoted, _ := json.Marshal(map[string]string{"checkpoint_json": strings.Repeat("x", mooring.MaxCanonicalBytes)})
	var out struct {
		Checkpoint string `json:"checkpoint_json"`
	}
	if len(quoted) <= mooring.MaxCanonicalBytes {
		t.Fatal("fixture not above legacy byte bound")
	}
	if commandNativeDecode(quoted, &out, "checkpoint_json") != nil {
		t.Fatal("private quoted overhead refused")
	}
	if _, e := mooring.CanonicalJSON(quoted); e == nil {
		t.Fatal("legacy bytes widened")
	}
	// Whitespace keeps a boundary fixture cheap to retain/canonicalize; its full
	// raw byte length is still admitted before the exact same frame decoder.
	at := bytes.Repeat([]byte(" "), commandNativeFrameLimit-3)
	at = append(at, []byte("[0]")...)
	if canonical, e := commandPrivateFrameJSON(at); e != nil || string(canonical) != "[0]" {
		t.Fatal("exact private frame byte bound refused")
	}
	if _, e := commandPrivateFrameJSON(append(at, ' ')); e == nil {
		t.Fatal("private frame overflow accepted")
	}
}

// One explicit local run measures the admitted worst-width structure fixture;
// timing is evidence, not a larger deadline or an interaction promise.
func BenchmarkCommandPrivateJSONStructureBoundary(b *testing.B) {
	array := []byte("[" + strings.Repeat("0,", commandNativeJSONStructureLimit-1) + "0]")
	var object strings.Builder
	object.Grow(commandCheckpointLimit)
	object.WriteByte('{')
	for i := 348999; i >= 0; i-- {
		if i != 348999 {
			object.WriteByte(',')
		}
		fmt.Fprintf(&object, "\"k%06d\":0", i)
	}
	object.WriteByte('}')
	for name, raw := range map[string][]byte{"array_at_structure_limit": array, "reverse_object_near_byte_limit": []byte(object.String())} {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := commandCheckpointJSON(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
