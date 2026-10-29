package mooring

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

type checkpointIdentityCase struct {
	ID                    string              `json:"id"`
	Checkpoint            protocol.Checkpoint `json:"checkpoint"`
	DecodedLimit          int                 `json:"decoded_limit"`
	PreimageLimit         int                 `json:"preimage_limit"`
	ExpectedDigest        string              `json:"expected_digest"`
	ExpectedContentDigest string              `json:"expected_content_digest"`
	ExpectedState         string              `json:"expected_state_base64"`
	ExpectedPreimage      string              `json:"expected_preimage"`
	Verify                bool                `json:"verify"`
}

func checkpointIdentityFixture(t *testing.T) ([]checkpointIdentityCase, []checkpointIdentityCase) {
	t.Helper()
	raw, err := os.ReadFile("../../mooring/protocol/checkpoint-identity-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases    []checkpointIdentityCase `json:"cases"`
		Rejected []checkpointIdentityCase `json:"rejected"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture.Cases, fixture.Rejected
}
func TestCheckpointIdentitySharedFullTupleAndRejectedVectors(t *testing.T) {
	cases, rejected := checkpointIdentityFixture(t)
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			identity, err := ComputeCheckpointIdentity(tc.Checkpoint, CheckpointIdentityLimits{tc.DecodedLimit, tc.PreimageLimit})
			if err != nil {
				t.Fatal(err)
			}
			if identity.FullDigest != tc.ExpectedDigest || identity.ContentDigest != tc.ExpectedContentDigest || base64.StdEncoding.EncodeToString(identity.State) != tc.ExpectedState {
				t.Fatal("shared identity mismatch")
			}
			if canonicalDigest([]byte(tc.ExpectedPreimage)) != identity.FullDigest {
				t.Fatal("independent preimage mismatch")
			}
			_, err = VerifyCheckpointIdentity(tc.Checkpoint, CheckpointIdentityLimits{tc.DecodedLimit, tc.PreimageLimit})
			if (err == nil) != tc.Verify {
				t.Fatal("identity verification mismatch", err)
			}
		})
	}
	for _, tc := range rejected {
		t.Run(tc.ID, func(t *testing.T) {
			if _, err := ComputeCheckpointIdentity(tc.Checkpoint, CheckpointIdentityLimits{tc.DecodedLimit, tc.PreimageLimit}); err == nil {
				t.Fatal("invalid shared checkpoint accepted")
			}
		})
	}
	if len(cases) != 15 || len(rejected) != 14 {
		t.Fatal("fixture coverage drift")
	}
}
func TestCheckpointIdentityLargeStateAssetsAndDirectTypedShapeBoundaries(t *testing.T) {
	cases, _ := checkpointIdentityFixture(t)
	cp := cases[0].Checkpoint
	raw := make([]byte, 1024*1024)
	cp.State = base64.StdEncoding.EncodeToString(raw)
	cp.Assets = make([]protocol.Asset, 4096)
	for i := range cp.Assets {
		cp.Assets[i] = protocol.Asset{AssetID: protocol.Identifier(fmt.Sprintf("asset-%04d", 4095-i)), Digest: protocol.Digest(cases[0].Checkpoint.Assets[0].Digest), SizeBytes: "18446744073709551615"}
	}
	limits := CheckpointIdentityLimits{8 * 1024 * 1024, 16 * 1024 * 1024}
	identity, err := ComputeCheckpointIdentity(cp, limits)
	if err != nil || !bytes.Equal(identity.State, raw) {
		t.Fatal("large generic identity", err)
	}
	for _, bad := range []string{"00", "-1", "18446744073709551616", "1.0"} {
		wrong := cases[0].Checkpoint
		wrong.Generation = protocol.Counter(bad)
		if _, err = ComputeCheckpointIdentity(wrong, limits); err == nil {
			t.Fatal("bad typed counter accepted")
		}
	}
	wrong := cases[0].Checkpoint
	wrong.Scope.ArtifactID = protocol.Identifier(string([]byte{255}))
	if _, err = ComputeCheckpointIdentity(wrong, limits); err == nil {
		t.Fatal("invalid UTF8 ID accepted")
	}
	wrong = cases[0].Checkpoint
	wrong.Assets = nil
	if _, err = ComputeCheckpointIdentity(wrong, limits); err == nil {
		t.Fatal("null manifest accepted")
	}
	wrong = cases[0].Checkpoint
	wrong.Encoding = "json"
	wrong.State = string([]byte{255})
	if _, err = ComputeCheckpointIdentity(wrong, limits); err == nil {
		t.Fatal("invalid UTF8 state accepted")
	}
}
