package mooring

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func privateValidateFixture(t *testing.T) []byte {
	t.Helper()
	checkpoint, err := encodePreparedCheckpoint([]byte(`{"application":"opaque"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.ValidateCheckpoint{Type: "validate_checkpoint", RequestID: "request", Scope: protocol.Scope{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"}, Selected: protocol.Versions{ProtocolVersion: "0.4.0", ModelVersion: "0.1.0", EngineVersion: "0.10.0"}, Checkpoint: checkpoint, ControlProfile: PrivatePrepareProfile})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestPrivateControlClosedDirectionAndStrictJSON(t *testing.T) {
	v, err := newPrivateControlValidator()
	if err != nil {
		t.Fatal(err)
	}
	raw := privateValidateFixture(t)
	if _, err = v.validate(raw, false); err != nil {
		t.Fatal(err)
	}
	if _, err = v.validate(raw, true); err == nil {
		t.Fatal("wrong direction accepted")
	}
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte(`"request_id":"request"`), []byte(`"request_id":"foreign","request_id":"request"`), 1),
		bytes.Replace(raw, []byte(`"control_profile":"mooring.prepare.v1"`), []byte(`"control_profile":"unknown"`), 1),
		append([]byte(`{"unknown":true,`), raw[1:]...),
		bytes.Replace(raw, []byte(`"type":"validate_checkpoint"`), []byte(`"type":"commit_batch"`), 1),
	} {
		if _, err = v.validate(bad, false); err == nil {
			t.Fatalf("invalid private shape accepted: %s", bad)
		}
	}
}
func TestPrivateControlDoesNotRelaxPublicAndBoundsCanonicalExpansion(t *testing.T) {
	v, err := newPrivateControlValidator()
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(privateValidateFixture(t), &value) != nil {
		t.Fatal("fixture")
	}
	c := value["checkpoint"].(map[string]any)
	c["state"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 400000))
	raw, _ := json.Marshal(value)
	if _, err = v.validate(raw, false); err != nil {
		t.Fatal("private D3-sized shape", err)
	}
	if _, err = CanonicalE3WireJSON(raw); err == nil {
		t.Fatal("public bound relaxed")
	}
	if _, err = v.validate(append(raw, bytes.Repeat([]byte(" "), MaxPrivatePrepareRPCFrame)...), false); err == nil {
		t.Fatal("raw overbound accepted")
	}
	// Ten thousand binary64 numbers each gain a plus sign during JCS. Both
	// inputs fit structural quotas; only the normalized output exceeds2MiB.
	prefix := `{"n":[` + strings.Repeat("1e99,", 9999) + `1e99],"p":"`
	suffix := `"}`
	raw = []byte(prefix + strings.Repeat("x", MaxPrivatePrepareRPCFrame-5000-len(prefix)-len(suffix)) + suffix)
	if canonicalPreflightLimit(raw, MaxPrivatePrepareRPCFrame) != nil {
		t.Fatal("expansion fixture not within raw bounds")
	}
	if _, err = canonicalPrivateControlJSON(raw); err == nil {
		t.Fatal("canonical/resource overbound accepted")
	}
}
func TestPrivateCheckpointCanonicalBase64AndDigest(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("a"), []byte("ab"), []byte("abc"), bytes.Repeat([]byte("x"), maxPreparedCheckpoint)} {
		c, err := encodePreparedCheckpoint(raw)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodePreparedCheckpoint(c)
		if err != nil || !bytes.Equal(decoded, raw) {
			t.Fatal("checkpoint roundtrip", err)
		}
	}
	for _, encoded := range []string{"e31=", "e3==", "e30=\n", "e=30", "e30", "e30===", "é30="} {
		c := protocol.OpaquePreparedCheckpoint{Encoding: "base64", State: encoded, Digest: protocol.Digest(canonicalDigest([]byte("{}")))}
		if _, err := decodePreparedCheckpoint(c); err == nil {
			t.Fatal("noncanonical base64 accepted", encoded)
		}
	}
	c, _ := encodePreparedCheckpoint([]byte("opaque"))
	c.Digest = protocol.Digest(canonicalDigest([]byte("foreign")))
	if _, err := decodePreparedCheckpoint(c); err == nil {
		t.Fatal("wrong checkpoint digest accepted")
	}
	if _, err := encodePreparedCheckpoint(make([]byte, maxPreparedCheckpoint+1)); err == nil {
		t.Fatal("checkpoint overlimit encoded")
	}
}
