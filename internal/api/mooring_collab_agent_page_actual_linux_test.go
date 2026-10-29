//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// The caller places this under the existing actual-tool top-level catalog.
// It needs the separately reviewed read-task fixture prerequisite. Original
// run/HTTP contexts remain host fixtures, not a model or a paid container.
func testMooringActualAgentD3PageReassembly(t *testing.T) {
	checkpoint, digest := os.Getenv("MOORING_D3_CHECKPOINT"), os.Getenv("MOORING_D3_CHECKPOINT_SHA256")
	if checkpoint == "" || digest == "" {
		t.Fatal("pinned actual D3 checkpoint required; never skip")
	}
	t.Setenv("MOORING_SESSION_CHECKPOINT", checkpoint)
	t.Setenv("MOORING_SESSION_CHECKPOINT_SHA256", digest)
	withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		call := func(query url.Values) *agentToolBufferedWriter {
			t.Helper()
			r := x.request()
			r.Method = http.MethodGet
			r.URL.RawQuery = query.Encode()
			w := newAgentToolBufferedWriter(false)
			m.agentTool(w, r)
			return w
		}
		whole := call(nil)
		var original struct {
			Native   json.RawMessage  `json:"native"`
			BlockIDs []string         `json:"block_ids"`
			Revision protocol.Counter `json:"revision"`
		}
		if whole.Code != 200 || !whole.flushed || json.Unmarshal(whole.Body.Bytes(), &original) != nil || len(original.BlockIDs) < 33 {
			t.Fatal("actual complete D3 source or physical flush missing")
		}
		query := url.Values{"page": {"1"}, "max_bytes": {"65536"}}
		var header map[string]json.RawMessage
		var body []json.RawMessage
		var ids []string
		for page := 0; ; page++ {
			if page > len(original.BlockIDs)+1 {
				t.Fatal("actual native cursor failed to advance")
			}
			w := call(query)
			var result protocol.ReadResult
			if w.Code != 200 || !w.flushed || w.Body.Len() > 65536 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Revision != original.Revision || len(result.Items) == 0 {
				t.Fatalf("actual page/Flush/revision/budget failed: page %d HTTP%d bytes%d", page, w.Code, w.Body.Len())
			}
			for _, item := range result.Items {
				var native string
				if json.Unmarshal(item.Data["native_json"], &native) != nil || !json.Valid([]byte(native)) {
					t.Fatal("missing lossless native JSON string")
				}
				switch item.ModelType {
				case "wordcraft.native-header-json.v1":
					if header != nil || page != 0 || json.Unmarshal([]byte(native), &header) != nil {
						t.Fatal("header repeated or invalid")
					}
				case "wordcraft.native-paragraph-json.v1":
					ids = append(ids, string(item.TargetID))
					body = append(body, json.RawMessage(native))
				default:
					t.Fatal("unknown native page item")
				}
			}
			if result.NextCursor == nil {
				if page < 1 {
					t.Fatal("D3 did not traverse multiple actual native requests")
				}
				break
			}
			query.Set("cursor", *result.NextCursor)
			query.Set("revision", string(result.Revision))
			if page == 0 {
				// A bad snapshot/revision must be a correlated refusal, with the
				// valid original read and its native process still usable.
				bad := url.Values{"page": {"1"}, "cursor": {`["unrelated-snapshot","stable-0"]`}, "revision": {string(result.Revision)}}
				denied := call(bad)
				if denied.Code != 409 || bytes.Contains(denied.Body.Bytes(), []byte("native_json")) || a.document.cell.runtime.Err() != nil {
					t.Fatal("bad cursor leaked content or destroyed live read")
				}
			}
		}
		if header == nil || !reflect.DeepEqual(ids, original.BlockIDs) {
			t.Fatal("actual stable native IDs changed across pages")
		}
		header["body"], _ = json.Marshal(body)
		reassembled, err := json.Marshal(header)
		if err != nil {
			t.Fatal(err)
		}
		decode := func(raw []byte) any {
			t.Helper()
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber() // Never round opaque application integers.
			var value any
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		if !reflect.DeepEqual(decode(reassembled), decode(original.Native)) {
			t.Fatal("actual D3 native content changed during page reassembly")
		}
	}, "read")
}
