package pipeline

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRunAdmissionProjectionWitnessVariants(t *testing.T) {
	for _, selection := range []uint8{1, 2, 3} {
		for _, shape := range []string{"native-NULL", "exact-TEXT", "raw-TEXT"} {
			t.Run(string(rune('0'+selection))+"/"+shape, func(t *testing.T) {
				v := admissionCodecFixture(selection)
				if shape == "raw-TEXT" {
					v.data.EffectiveDefinition = []byte{'x', 0xff, 0, '\n'}
					admissionCodecRehash(&v)
				}
				if shape != "native-NULL" {
					v.data.ExecutedProjection = admissionNativeBytes{Kind: 1, Bytes: bytes.Clone(v.data.EffectiveDefinition)}
				}
				raw, err := encodeRunAdmissionV1(v, 1<<20)
				if err != nil {
					t.Fatalf("encode witness: %v", err)
				}
				got, err := decodeRunAdmissionV1(raw, len(raw))
				if err != nil || !reflect.DeepEqual(got, v) {
					t.Fatalf("native witness changed: %v", err)
				}
			})
		}
	}
}

func TestRunAdmissionProjectionWitnessRejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value admissionNativeBytes
	}{
		{"NULL-with-empty-bytes", admissionNativeBytes{Kind: 0, Bytes: make([]byte, 0)}},
		{"NULL-with-value", admissionNativeBytes{Kind: 0, Bytes: []byte("raw")}},
		{"TEXT-nil", admissionNativeBytes{Kind: 1}},
		{"TEXT-empty", admissionNativeBytes{Kind: 1, Bytes: make([]byte, 0)}},
		{"TEXT-mismatch", admissionNativeBytes{Kind: 1, Bytes: []byte("other raw")}},
		{"BLOB-exact-bytes", admissionNativeBytes{Kind: 2, Bytes: []byte("effective raw\n")}},
		{"BLOB-empty", admissionNativeBytes{Kind: 2, Bytes: make([]byte, 0)}},
		{"unknown-kind", admissionNativeBytes{Kind: 3, Bytes: []byte("effective raw\n")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := admissionCodecFixture(1)
			v.data.ExecutedProjection = tc.value
			if raw, err := encodeRunAdmissionV1(v, 1<<20); raw != nil || !errors.Is(err, errRunAdmissionUnavailable) {
				t.Fatalf("invalid witness encoded: %v", err)
			}
			if got, err := decodeRunAdmissionV1(admissionCodecWire(t, v), 1<<20); !reflect.DeepEqual(got, runAdmissionV1{}) || !errors.Is(err, errRunAdmissionUnavailable) {
				t.Fatalf("invalid witness decoded: %v", err)
			}
		})
	}
}

func TestRunAdmissionProjectionWitnessNullNeverCollapsesCapturedText(t *testing.T) {
	captured := admissionCodecFixture(1)
	captured.data.ExecutedProjection = admissionNativeBytes{Kind: 1, Bytes: bytes.Clone(captured.data.EffectiveDefinition)}
	capturedWire, err := encodeRunAdmissionV1(captured, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	noProjection := admissionCodecFixture(1)
	nullWire, err := encodeRunAdmissionV1(noProjection, 1<<20)
	if err != nil || bytes.Equal(capturedWire, nullWire) {
		t.Fatalf("capture shapes collapsed: %v", err)
	}
	got, err := decodeRunAdmissionV1(capturedWire, 1<<20)
	if err != nil || got.data.ExecutedProjection.Kind != 1 || !bytes.Equal(got.data.ExecutedProjection.Bytes, got.data.EffectiveDefinition) {
		t.Fatalf("captured TEXT erased by roundtrip: %v", err)
	}
	got, err = decodeRunAdmissionV1(nullWire, 1<<20)
	if err != nil || got.data.ExecutedProjection.Kind != 0 || got.data.ExecutedProjection.Bytes != nil {
		t.Fatalf("native NULL changed by roundtrip: %v", err)
	}
	// Omission is not an implicit native-NULL witness. This checks grammar;
	// comparing a stored receipt to a later SQL erasure is a future reader gate.
	missing := strings.Replace(string(nullWire), `"executed_projection":{"kind":0,"bytes":null},`, "", 1)
	if missing == string(nullWire) {
		t.Fatal("witness omission setup did not change bytes")
	}
	if got, err := decodeRunAdmissionV1([]byte(missing), 1<<20); !reflect.DeepEqual(got, runAdmissionV1{}) || !errors.Is(err, errRunAdmissionUnavailable) {
		t.Fatalf("missing witness decoded: %v", err)
	}
}
