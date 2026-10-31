package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/gorilla/websocket"
)

// The collaboration submit path tells every browser socket that aggregate
// artifact storage is full (close 4009, the Design code) and ignores any other
// failure, which keeps the generic teardown.
func TestCollaborationSubmitStorageRefusalClosesSocketsWith4009(t *testing.T) {
	quota := errors.Join(artifacts.ErrStorageQuota, errors.New("constraint failed: artifact_storage_quota"))
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"aggregate quota", quota, true},
		{"wrapped aggregate quota", errors.Join(errors.New("commit"), quota), true},
		{"page history quota only", artifacts.ErrVersionQuota, false},
		{"generic failure", errors.New("boom"), false},
		{"nil", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			closed := make(chan *websocket.CloseError, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				d := &collaborationHTTPDocument{connections: map[string]*collaborationHTTPConnection{"c1": {socket: ws}, "c2": {}}}
				if got := d.refuseStorageFull(c.err); got != c.want {
					t.Errorf("refuseStorageFull = %v, want %v", got, c.want)
				}
				time.Sleep(200 * time.Millisecond)
				_ = ws.Close()
			}))
			defer server.Close()
			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			go func() {
				_, _, err := client.ReadMessage()
				var ce *websocket.CloseError
				if errors.As(err, &ce) {
					closed <- ce
				} else {
					closed <- nil
				}
			}()
			select {
			case ce := <-closed:
				if c.want && (ce == nil || ce.Code != 4009 || ce.Text != "Artifact storage capacity full") {
					t.Fatalf("close = %+v", ce)
				}
				if !c.want && ce != nil && ce.Code == 4009 {
					t.Fatal("unrelated failure reported as storage full")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no close")
			}
		})
	}
}
