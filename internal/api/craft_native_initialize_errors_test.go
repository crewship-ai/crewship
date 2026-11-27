package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type brokenOwnerClock struct{}

func (brokenOwnerClock) Now() (string, time.Duration, error) { return "", 0, errors.New("no boot id") }

// A server fault while initializing is not "access denied": the owner of an empty page gets a
// 5xx with a code, and the log names the failing step and the error's class, never its text.
func TestCraftNativeInitializeTellsAServerFaultFromAnAccessRefusal(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	d, _, ctx, page := importFixture(t, "owner", okConvert)
	var started struct{ ID string }
	_ = json.Unmarshal(startImport(d, ctx, page, []byte("PK")).Body.Bytes(), &started)
	waitImportState(t, d, ctx, page, started.ID)
	d.human.clock = brokenOwnerClock{}
	out := importInitializeOf(d, ctx, page, started.ID)
	var got struct{ Code string }
	_ = json.Unmarshal(out.Body.Bytes(), &got)
	if out.Code != 503 || got.Code != "clock" {
		t.Fatalf("broken clock: %d %s", out.Code, out.Body.String())
	}
	if l := logs.String(); !strings.Contains(l, "step=clock") || strings.Contains(l, "no boot id") {
		t.Fatalf("log: %s", l)
	}
}

// A real refusal stays 403 and is logged with its step.
func TestCraftNativeInitializeStillRefusesAccessWith403(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	d, _, ctx, page := importFixture(t, "nonowner-writer", okConvert)
	out := importInitializeOf(d, ctx, page, strings.Repeat("1", 64))
	if out.Code != 404 {
		t.Fatalf("unknown import: %d", out.Code)
	}
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"application_id":"gridcraft","bytes":"e30="}`)).WithContext(ctx)
	req.SetPathValue("slug", "@"+page)
	rec := httptest.NewRecorder()
	d.initialize(rec, req)
	if rec.Code != 403 {
		t.Fatalf("not the owner: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "step=") {
		t.Fatalf("refusal not logged: %s", logs.String())
	}
}
