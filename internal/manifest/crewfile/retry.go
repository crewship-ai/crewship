package crewfile

import (
	"context"
	"net/http"
	"time"
)

// ConflictBackoff bounds the wait for a recycling crew container (about 30 s
// in total). A var so tests can shorten it.
var ConflictBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

// SaveWithRecycleWait runs save until it answers something other than 409 or
// ConflictBackoff runs out. save returns the HTTP status it got (0 for a
// transport error) and the error to report for it, nil on success.
//
// A file save answers 409 when the file is owned by the crew runtime and the
// container is not running. Right after a crew PATCH in the same apply that is
// the asynchronous container recycle, not a stopped crew, so both apply paths
// wait a bounded time for it instead of failing on the first answer. Other
// statuses, transport errors and a cancelled context are not retried.
func SaveWithRecycleWait(ctx context.Context, save func() (status int, err error)) error {
	for attempt := 0; ; attempt++ {
		status, err := save()
		if err == nil || status != http.StatusConflict || attempt >= len(ConflictBackoff) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ConflictBackoff[attempt]):
		}
	}
}
