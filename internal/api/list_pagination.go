package api

import (
	"fmt"
	"net/http"
	"strconv"
)

const maxRoutineListPageSize = 500

// routineListPage parses the additive limit/offset contract used by routine
// catalog endpoints. A nil limit preserves an endpoint's legacy default.
func routineListPage(w http.ResponseWriter, rawLimit, rawOffset string, defaultLimit int) (limit, offset int, ok bool) {
	limit = defaultLimit
	if rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > maxRoutineListPageSize {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxRoutineListPageSize))
			return 0, 0, false
		}
		limit = parsed
	}
	if rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 || parsed > 1_000_000 {
			replyError(w, http.StatusBadRequest, "offset must be between 0 and 1000000")
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}

func setRoutineListNextOffset(w http.ResponseWriter, offset, limit, count int) {
	if limit > 0 && count == limit {
		w.Header().Set("X-Next-Offset", strconv.Itoa(offset+count))
	}
}
