package kinds

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/manifest/crewfile"
	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// scriptedPutBytes answers PutBytes from a fixed sequence of statuses with an
// error body, repeating the last one.
type scriptedPutBytes struct {
	*crewFakeClient
	statuses []int
	calls    int
}

func (s *scriptedPutBytes) PutBytes(_ context.Context, _ string, _ []byte) (*internalapi.Response, error) {
	st := s.statuses[min(s.calls, len(s.statuses)-1)]
	s.calls++
	if st >= 200 && st < 300 {
		return crewJSONResp(st, map[string]any{"status": "saved"}), nil
	}
	if st == 409 {
		return crewJSONResp(st, map[string]any{"error": "file is owned by the crew runtime; it can only be overwritten while the crew container is running."}), nil
	}
	return crewJSONResp(st, map[string]any{"error": "file exceeds 1 byte limit"}), nil
}

// The standalone kind:Crew path PATCHes the crew (which recycles its
// container asynchronously) and then saves its files, like the legacy path.
// A save in the recycle window answers 409; it must wait a bounded time for
// the container instead of failing the apply on the first answer.
func TestPutCrewFile_ConflictDuringRecycle(t *testing.T) {
	old := crewfile.ConflictBackoff
	crewfile.ConflictBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { crewfile.ConflictBackoff = old }()

	for _, tc := range []struct {
		name      string
		statuses  []int
		wantCalls int
		wantErr   []string
	}{
		{"recycle finishes within the wait", []int{409, 409, 200}, 3, nil},
		{"still stopped after the wait keeps the server reason", []int{409}, 3, []string{"409", "crew container is running"}},
		{"other errors are not retried", []int{413}, 1, []string{"413", "exceeds"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &scriptedPutBytes{crewFakeClient: newCrewFake(), statuses: tc.statuses}
			err := putCrewFile(context.Background(), c, "c1", "shared/x", []byte("d"))
			if c.calls != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", c.calls, tc.wantCalls)
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("putCrewFile: %v", err)
				}
				return
			}
			for _, w := range tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Fatalf("err = %v, want it to contain %q", err, w)
				}
			}
		})
	}
}

// A cancelled apply stops waiting for the container.
func TestPutCrewFile_ConflictWaitHonoursCancel(t *testing.T) {
	old := crewfile.ConflictBackoff
	crewfile.ConflictBackoff = []time.Duration{time.Hour}
	defer func() { crewfile.ConflictBackoff = old }()

	ctx, cancel := context.WithCancel(context.Background())
	c := &scriptedPutBytes{crewFakeClient: newCrewFake(), statuses: []int{409}}
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := putCrewFile(ctx, c, "c1", "shared/x", []byte("d"))
	if err != context.Canceled || c.calls != 1 {
		t.Fatalf("err=%v calls=%d, want context.Canceled after one attempt", err, c.calls)
	}
}
