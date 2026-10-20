package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const historyRestorePacket = `{"page_id":"human-doc-page","id":"restore-one","schema":3,"epoch":"original-epoch","ifRev":8,"checkpoint":"pin-one"}`

// This tests the public projection and original credential forwarding, not the
// real engine. Store restore atomicity/epoch tests remain separate evidence.
func TestHumanDocumentHistoryRestoreBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, result   string
		upstream, want int
		failed         bool
	}{
		{"confirmed", `{"id":"restore-one","rev":9,"epoch":"next-epoch","actor":"user:writer","kind":"restore","schema":2,"snapshot":"ENGINE_PRIVATE"}`, 200, 200, false},
		{"wrong operation", `{"id":"other","rev":9,"epoch":"next-epoch","kind":"restore","schema":2}`, 200, 502, false},
		{"non advancing revision", `{"id":"restore-one","rev":8,"epoch":"next-epoch","kind":"restore","schema":2}`, 200, 502, false},
		{"wrong kind", `{"id":"restore-one","rev":9,"epoch":"next-epoch","kind":"patch","schema":2}`, 200, 502, false},
		{"future schema", `{"id":"restore-one","rev":9,"epoch":"next-epoch","kind":"restore","schema":99}`, 200, 409, false},
		{"denied", `{"rev":9,"snapshot":"ENGINE_PRIVATE"}`, 403, 403, false},
		{"conflict", `{"rev":9,"snapshot":"ENGINE_PRIVATE"}`, 409, 409, false},
		{"uncertain", `{"id":"restore-one","rev":9,"epoch":"next-epoch","kind":"restore","schema":2}`, 200, 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, workspace, human := humanDocumentTransportFixture(t)
			calls := 0
			h.documentCall = func(_ context.Context, ws, bearer, page, operation string, input any) ([]byte, int, error) {
				calls++
				if ws != workspace || bearer != "Bearer original-human-credential" || page != "human-doc-page" || operation != "restore" {
					t.Fatal("restore changed original identity")
				}
				got, _ := json.Marshal(input)
				var a, b any
				if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(historyRestorePacket), &b) != nil {
					t.Fatal("invalid packet")
				}
				if !reflect.DeepEqual(a, b) {
					t.Fatal("restore changed exact original packet")
				}
				var err error
				if tc.failed {
					err = errors.New("unconfirmed result")
				}
				return []byte(tc.result), tc.upstream, err
			}
			w := httptest.NewRecorder()
			h.RestoreDocumentCheckpoint(w, humanDocumentRequest(t, "POST", "/restore", workspace, human, historyRestorePacket))
			if w.Code != tc.want || calls != 1 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d calls=%d", w.Code, calls)
			}
			if strings.Contains(w.Body.String(), "ENGINE_PRIVATE") || (tc.want != 200 && strings.Contains(w.Body.String(), `"rev"`)) {
				t.Fatal("failed restore published protected result")
			}
			if tc.want == 200 {
				var value map[string]any
				if json.Unmarshal(w.Body.Bytes(), &value) != nil || value["page_id"] != "human-doc-page" || value["id"] != "restore-one" || value["kind"] != "restore" {
					t.Fatal("missing closed restore receipt")
				}
			}
		})
	}
}

func TestHumanDocumentHistoryRestoreRejectsChangedScope(t *testing.T) {
	for _, body := range []string{
		strings.Replace(historyRestorePacket, "human-doc-page", "other-page", 1),
		strings.Replace(historyRestorePacket, `"schema":3`, `"schema":99`, 1),
		strings.Replace(historyRestorePacket, `"ifRev":8`, `"ifRev":-1`, 1),
		strings.Replace(historyRestorePacket, `"id":"restore-one"`, `"id":"restore-one","id":"other"`, 1),
		strings.TrimSuffix(historyRestorePacket, "}") + `,"run_id":"old-run"}`,
	} {
		h, workspace, human := humanDocumentTransportFixture(t)
		h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
			t.Fatal("invalid packet reached engine")
			return nil, 500, nil
		}
		w := httptest.NewRecorder()
		h.RestoreDocumentCheckpoint(w, humanDocumentRequest(t, "POST", "/restore", workspace, human, body))
		if w.Code != 400 {
			t.Fatalf("invalid packet status=%d", w.Code)
		}
	}
}
