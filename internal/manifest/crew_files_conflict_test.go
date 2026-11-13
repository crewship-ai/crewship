package manifest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// scriptedPutAPI answers PutBytes from a fixed sequence of (status, body).
type scriptedPutAPI struct {
	APIClient
	replies []struct {
		status int
		body   string
	}
	calls int
}

func (s *scriptedPutAPI) PutBytes(_ context.Context, _ string, _ []byte) (*http.Response, error) {
	r := s.replies[min(s.calls, len(s.replies)-1)]
	s.calls++
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body))}, nil
}

const recycle409 = `{"error":"file is owned by the crew runtime; it can only be overwritten while the crew container is running."}`

// A device-monitoring apply PATCHed the crew (which recycles its container
// asynchronously) and then uploaded files into the shared tree: the upload
// hit 409 and the operator saw only "HTTP 409". A recycle that finishes
// within the bounded wait now succeeds; one that does not reports the
// server's reason.
func TestSaveCrewFile_ConflictDuringRecycle(t *testing.T) {
	old := crewFileConflictBackoff
	crewFileConflictBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { crewFileConflictBackoff = old }()

	t.Run("recycle finishes within the wait", func(t *testing.T) {
		api := &scriptedPutAPI{}
		api.replies = append(api.replies, struct {
			status int
			body   string
		}{409, recycle409}, struct {
			status int
			body   string
		}{200, `{"status":"saved"}`})
		if err := NewClient(api).SaveCrewFile(context.Background(), "c1", "shared/x", []byte("d")); err != nil {
			t.Fatalf("SaveCrewFile: %v", err)
		}
		if api.calls != 2 {
			t.Fatalf("calls = %d, want 2", api.calls)
		}
	})
	t.Run("still stopped after the wait keeps the server reason", func(t *testing.T) {
		api := &scriptedPutAPI{}
		api.replies = append(api.replies, struct {
			status int
			body   string
		}{409, recycle409})
		err := NewClient(api).SaveCrewFile(context.Background(), "c1", "shared/x", []byte("d"))
		if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "crew container is running") {
			t.Fatalf("err = %v, want 409 with the server's reason", err)
		}
		if api.calls != 3 {
			t.Fatalf("calls = %d, want 1 + 2 bounded retries", api.calls)
		}
	})
	t.Run("other errors are not retried", func(t *testing.T) {
		api := &scriptedPutAPI{}
		api.replies = append(api.replies, struct {
			status int
			body   string
		}{413, `{"error":"file exceeds 1 byte limit"}`})
		err := NewClient(api).SaveCrewFile(context.Background(), "c1", "shared/x", []byte("d"))
		if err == nil || !strings.Contains(err.Error(), "exceeds") || api.calls != 1 {
			t.Fatalf("err=%v calls=%d, want one attempt with the server's reason", err, api.calls)
		}
	})
}
