package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	xwebsocket "golang.org/x/net/websocket"
)

func TestCollaborationSocketLiveness(t *testing.T) {
	for _, responsive := range []bool{true, false} {
		name := "silent-peer-expires"
		if responsive {
			name = "idle-responsive-peer-survives"
		}
		t.Run(name, func(t *testing.T) {
			result := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
				if err != nil {
					result <- err
					return
				}
				defer ws.Close()
				stop := collaborationSocketLiveness(context.Background(), ws, 200*time.Millisecond, 25*time.Millisecond)
				defer stop()
				_, raw, err := ws.ReadMessage()
				if err == nil && string(raw) != "after-idle" {
					t.Errorf("unexpected application packet %q", raw)
				}
				result <- err
			}))
			defer server.Close()
			// Existing x/net clients, like the actual integration fixtures, process
			// transport ping/pong while waiting for their next application message.
			ws, err := xwebsocket.Dial("ws"+strings.TrimPrefix(server.URL, "http"), "", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			if responsive {
				readDone := make(chan struct{})
				go func() { defer close(readDone); var raw []byte; _ = xwebsocket.Message.Receive(ws, &raw) }()
				// Three full idle bounds without any application traffic.
				timer := time.NewTimer(600 * time.Millisecond)
				defer timer.Stop()
				select {
				case err := <-result:
					t.Fatalf("responsive idle peer closed: %v", err)
				case <-timer.C:
				}
				if err := xwebsocket.Message.Send(ws, "after-idle"); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("responsive peer stalled")
				}
				ws.Close()
				<-readDone
			} else {
				// No reader means no transport pong; unrelated data is never fabricated.
				select {
				case err := <-result:
					if err == nil {
						t.Fatal("silent peer did not expire")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("silent peer outlived idle bound")
				}
			}
		})
	}
}
