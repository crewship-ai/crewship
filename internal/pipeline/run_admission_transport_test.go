package pipeline

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunAdmissionTransportExactOuterTuple(t *testing.T) {
	for _, kind := range []uint8{1, 2, 3} {
		v := admissionCodecFixture(kind)
		raw, err := encodeRunAdmissionV1(v, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		before := bytes.Clone(raw)
		if err := CheckRunAdmissionPayload(raw, len(raw), "receipt", "workspace", "pipeline", "run"); err != nil {
			t.Fatalf("exact structural shape %d rejected: %v", kind, err)
		}
		if !bytes.Equal(before, raw) {
			t.Fatal("validation modified stored bytes")
		}
		for i := 0; i < 4; i++ {
			tuple := []string{"receipt", "workspace", "pipeline", "run"}
			tuple[i] = "foreign"
			if err := CheckRunAdmissionPayload(raw, len(raw), tuple[0], tuple[1], tuple[2], tuple[3]); err == nil {
				t.Fatalf("outer tuple component %d ignored", i)
			}
		}
	}
}
func TestRunAdmissionTransportBudgetAndPrivateFailure(t *testing.T) {
	raw, err := encodeRunAdmissionV1(admissionCodecFixture(1), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{0, -1, len(raw) - 1} {
		if CheckRunAdmissionPayload(raw, budget, "receipt", "workspace", "pipeline", "run") == nil {
			t.Fatal("explicit buffer bound ignored")
		}
	}
	secret := "synthetic-human-private-field"
	malformed := append([]byte(`{"`+secret+`":1,`), raw[1:]...)
	for _, bad := range [][]byte{nil, []byte("{}"), malformed, append(bytes.Clone(raw), ' ')} {
		err := CheckRunAdmissionPayload(bad, len(bad), "receipt", "workspace", "pipeline", "run")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe structural error: %v", err)
		}
	}
}
func TestRunAdmissionTransportOpaqueIdentityBytes(t *testing.T) {
	v := admissionCodecFixture(1)
	v.data.ID = []byte{0xff}
	raw, err := encodeRunAdmissionV1(v, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckRunAdmissionPayload(raw, len(raw), string([]byte{0xff}), "workspace", "pipeline", "run"); err != nil {
		t.Fatal(err)
	}
	if CheckRunAdmissionPayload(raw, len(raw), string([]byte{0xfe}), "workspace", "pipeline", "run") == nil {
		t.Fatal("invalid UTF8 outer IDs collided")
	}
}
