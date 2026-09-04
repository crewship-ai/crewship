package api

import (
	"net/http/httptest"
	"testing"
)

// writeListMeta is the one place the S1 paging headers are spelled; every
// paged list calls it, so the names are pinned here once.
func TestWriteListMeta_SetsTheThreePagingHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	writeListMeta(rr, 1015, 50, 100)

	for name, want := range map[string]string{
		"X-Total-Count": "1015",
		"X-Limit":       "50",
		"X-Offset":      "100",
	} {
		if got := rr.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

// A zero total is still an answer ("nothing matched"), distinct from the
// header being absent ("this list does not page").
func TestWriteListMeta_ZeroTotalIsExplicit(t *testing.T) {
	rr := httptest.NewRecorder()
	writeListMeta(rr, 0, 20, 0)
	if got := rr.Header().Get("X-Total-Count"); got != "0" {
		t.Fatalf("X-Total-Count = %q, want \"0\"", got)
	}
}
