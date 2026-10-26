//go:build linux

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

type sessionFailureLogBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *sessionFailureLogBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *sessionFailureLogBuffer) snapshot() string {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.String()
}

func TestCollaborationSessionPumpFailureSeparatesCallbackMarshalAndReply(t *testing.T) {
	for _, stage := range []string{"pump-callback", "pump-response-marshal", "pump-reply-control"} {
		t.Run(stage, func(t *testing.T) {
			var output sessionFailureLogBuffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			r := newPumpTestRig(t, "normal", "error")
			pumpWait(t, r.ctx, r.entered)
			var response any = map[string]string{"not": "a protocol reply"}
			var callbackError error
			if stage == "pump-callback" {
				callbackError = errors.New("private-error-document-ticket")
			}
			if stage == "pump-response-marshal" {
				response = make(chan int)
			}
			if err := r.pump.reply(context.Background(), mooring.SessionControl{}, response, callbackError, "session_submit_batch"); err == nil {
				t.Fatal("fault was not returned")
			}
			close(r.release)
			pumpWait(t, r.ctx, r.runtime.Done())
			count := 0
			for _, line := range strings.Split(strings.TrimSpace(output.snapshot()), "\n") {
				var row map[string]any
				if json.Unmarshal([]byte(line), &row) != nil {
					t.Fatal(line)
				}
				if row["msg"] != "Mooring session first failure" || row["runtime_ref"] != r.runtime.RuntimeRef() {
					continue
				}
				count++
				if row["stage"] != stage || row["control_kind"] != "session_submit_batch" {
					t.Fatal(row)
				}
			}
			if count != 1 || strings.Contains(output.snapshot(), "private-error-document-ticket") {
				t.Fatal(output.snapshot())
			}
		})
	}
}
