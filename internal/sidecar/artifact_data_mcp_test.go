package sidecar

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactDataMCPPacketKeepsExactCASAndRejectsIdentity(t *testing.T) {
	valid := `{"slug":"demo","collection":"sales","base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"n":9007199254740993}}]}`
	packet, handled, err := artifactProjectMCPPacket("artifact_data_batch", []byte(valid))
	if err != nil || !handled || !bytes.Contains(packet, []byte(`9007199254740993`)) {
		t.Fatalf("packet changed value: %s %v", packet, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"ifVersion":0,`, "", 1),
		strings.Replace(valid, `"ifVersion":0`, `"ifVersion":0,"IfVersion":1`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":1,"BASE_VERSION":2`, 1),
		strings.Replace(valid, `"ifVersion":0`, `"ifVersion":9007199254740993`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":0`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":1,"updated_by":"spoof"`, 1),
		strings.Replace(valid, `"collection":"sales"`, `"collection":"../other"`, 1),
	} {
		if _, _, err := artifactProjectMCPPacket("artifact_data_batch", []byte(raw)); err == nil {
			t.Errorf("invalid packet accepted: %s", raw)
		}
	}
}

func TestArtifactDataMCPEnvelopeDoesNotExpandHTMLLikeValues(t *testing.T) {
	for _, value := range []string{strings.Repeat("<", 1<<20), strings.Repeat(string([]rune{0x2028, 0x2029}), 25000)} {
		w := httptest.NewRecorder()
		s := &Server{}
		body := []byte(`{"value":"` + value + `"}`)
		s.writeArtifactDataMCPResult(w, memoryMCPRequest{ID: json.RawMessage(`1`)}, 200, body)
		if w.Code != 200 || w.Body.Len() > len(body)+1024 {
			t.Fatalf("data envelope expanded: %d -> %d bytes", len(body), w.Body.Len())
		}
	}

}

func TestArtifactDataMCPBoundaryValueKeepsWireBudget(t *testing.T) {
	value := strings.Repeat("<", 200<<10)
	raw := []byte(`{"slug":"demo","collection":"sales","base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"DisplayName":"` + value + `","optional":null,"huge":1e400}}]}`)
	packet, handled, err := artifactProjectMCPPacket("artifact_data_batch", raw)
	if err != nil || !handled || len(packet) > len(raw)+1024 || bytes.Contains(packet, []byte(`\u003c`)) {
		t.Fatalf("valid data expanded or rejected: %d %v", len(packet), err)
	}
}
