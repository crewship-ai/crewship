package mooring

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	protocolschema "github.com/crewship-ai/crewship/mooring/protocol"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"github.com/gowebpki/jcs"
)

const maxPreparedCheckpoint = 1572864

var ErrPrivateControl = errors.New("invalid Mooring private control message")

// privateControlValidator accepts only the negotiated inherited-channel profile.
// Direction and resources convey no scope/operation authority; the adapter binds
// every response to its outstanding host request before returning a candidate.
type privateControlValidator struct{ schema *WireSchema }

func newPrivateControlValidator() (*privateControlValidator, error) {
	s, err := NewWireSchema(protocolschema.JSON())
	if err != nil {
		return nil, err
	}
	return &privateControlValidator{s}, nil
}

func (v *privateControlValidator) validate(raw []byte, fromRuntime bool) ([]byte, error) {
	if v == nil || v.schema == nil {
		return nil, ErrPrivateControl
	}
	normalized, err := canonicalPrivateControlJSON(raw)
	if err != nil {
		return nil, ErrPrivateControl
	}
	var value map[string]any
	if json.Unmarshal(normalized, &value) != nil || value["control_profile"] != PrivatePrepareProfile {
		return nil, ErrPrivateControl
	}
	t, _ := value["type"].(string)
	allowed := t == "validate_checkpoint" || t == "prepare_atomic"
	if fromRuntime {
		allowed = t == "checkpoint_validated" || t == "atomic_prepared"
	}
	if !allowed || !v.schema.matches(v.schema.root, value, 0) {
		return nil, ErrPrivateControl
	}
	return normalized, nil
}

func canonicalPrivateControlJSON(raw []byte) ([]byte, error) {
	if canonicalPreflightLimit(raw, MaxPrivatePrepareRPCFrame) != nil {
		return nil, ErrPrivateControl
	}
	normalized, err := jcs.Transform(raw)
	if err != nil || canonicalPreflightLimit(normalized, MaxPrivatePrepareRPCFrame) != nil {
		return nil, ErrPrivateControl
	}
	return normalized, nil
}

func encodePreparedCheckpoint(raw []byte) (protocol.OpaquePreparedCheckpoint, error) {
	if len(raw) > maxPreparedCheckpoint {
		return protocol.OpaquePreparedCheckpoint{}, ErrPrivateControl
	}
	return protocol.OpaquePreparedCheckpoint{Encoding: "base64", State: base64.StdEncoding.EncodeToString(raw), Digest: protocol.Digest(canonicalDigest(raw))}, nil
}

// Strict padding/pad bits and size are checked before allocating decoded bytes.
func decodePreparedCheckpoint(c protocol.OpaquePreparedCheckpoint) ([]byte, error) {
	if c.Encoding != "base64" || len(c.State)%4 != 0 || len(c.State) > ((maxPreparedCheckpoint+2)/3)*4 {
		return nil, ErrPrivateControl
	}
	padding := 0
	for i := len(c.State) - 1; i >= 0 && c.State[i] == '='; i-- {
		padding++
	}
	if padding > 2 || strings.ContainsAny(c.State, "\r\n") || len(c.State)/4*3-padding > maxPreparedCheckpoint {
		return nil, ErrPrivateControl
	}
	for i := 0; i < len(c.State); i++ {
		b := c.State[i]
		if i >= len(c.State)-padding {
			if b != '=' {
				return nil, ErrPrivateControl
			}
			continue
		}
		if b == '=' || base64Sextet(b) < 0 {
			return nil, ErrPrivateControl
		}
	}
	if padding == 1 && base64Sextet(c.State[len(c.State)-2])&3 != 0 {
		return nil, ErrPrivateControl
	}
	if padding == 2 && base64Sextet(c.State[len(c.State)-3])&15 != 0 {
		return nil, ErrPrivateControl
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(c.State)
	if err != nil || canonicalDigest(raw) != string(c.Digest) {
		return nil, ErrPrivateControl
	}
	return raw, nil
}

func base64Sextet(b byte) int {
	switch {
	case b >= 'A' && b <= 'Z':
		return int(b - 'A')
	case b >= 'a' && b <= 'z':
		return int(b-'a') + 26
	case b >= '0' && b <= '9':
		return int(b-'0') + 52
	case b == '+':
		return 62
	case b == '/':
		return 63
	}
	return -1
}
