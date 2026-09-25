package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestHostResourcesWindowAndMissingHistory(t *testing.T) {
	db := setupTestDB(t)
	h := hostResourcesHandler{db: db, logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	request := func(window string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		h.Resources(rr, httptest.NewRequest(http.MethodGet, "/api/v1/system/resources?window="+window, nil))
		return rr
	}
	if got := request("other").Code; got != http.StatusBadRequest {
		t.Fatalf("invalid window = %d, want 400", got)
	}
	var empty struct {
		Latest         *hostResourceSample `json:"latest"`
		RecordingSince *string             `json:"recording_since"`
	}
	if err := json.Unmarshal(request("24h").Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Latest != nil || empty.RecordingSince != nil {
		t.Fatalf("empty history claimed a measurement: %+v", empty)
	}

	now := time.Now().UTC()
	for _, sample := range []struct {
		at  time.Time
		cpu float64
	}{{now.Add(-48 * time.Hour), 25}, {now.Add(-2 * time.Minute), 40}} {
		if _, err := db.Exec(`INSERT INTO host_resource_samples(ts, cpu_percent, memory_percent, memory_used_mb, memory_total_mb) VALUES(?, ?, 50, 512, 1024)`, sample.at.Format(hostResourceTimeFormat), sample.cpu); err != nil {
			t.Fatal(err)
		}
	}
	var out struct {
		Latest         *hostResourceSample  `json:"latest"`
		RecordingSince *string              `json:"recording_since"`
		Series         []hostResourceBucket `json:"series"`
	}
	rr := request("24h")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Latest == nil || out.Latest.CPUPercent != 40 || out.RecordingSince == nil {
		t.Fatalf("latest or recording start incorrect: %+v", out)
	}
	measured := 0
	for _, bucket := range out.Series {
		if bucket.CPUPercent != nil {
			measured++
			if *bucket.CPUPercent != 40 {
				t.Fatalf("24h included an older measurement: %v", *bucket.CPUPercent)
			}
		}
	}
	if measured != 1 {
		t.Fatalf("24h measured buckets = %d, want 1", measured)
	}
}
