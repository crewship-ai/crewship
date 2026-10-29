package api

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

type publicationTestDBError int

func (e publicationTestDBError) Error() string { return "fixture SQLite error" }
func (e publicationTestDBError) Code() int     { return int(e) }

func TestPublicationWriteContentionStatus(t *testing.T) {
	for _, tc := range []struct {
		code, status int
	}{{5, 409}, {517, 409}, {6, 409}, {1, 500}} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			rr := httptest.NewRecorder()
			guestRoutineDraftError(rr, fmt.Errorf("write transaction: %w", publicationTestDBError(tc.code)))
			if rr.Code != tc.status {
				t.Errorf("SQLite write error %d: HTTP%d want%d", tc.code, rr.Code, tc.status)
			}
		})
	}
}

func TestPublishedCallSaveStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	if !replyScheduleConflict(rr, fmt.Errorf("save: %w", pipeline.ErrInvalidTrigger)) || rr.Code != 422 {
		t.Fatalf("published call save must fail with 422: %d", rr.Code)
	}
}
