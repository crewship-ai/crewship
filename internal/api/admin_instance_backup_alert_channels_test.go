package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/notify"
)

type alertSettingsView struct {
	Channels          []string                  `json:"channels"`
	AvailableChannels []backupplan.AlertChannel `json:"available_channels"`
	ChannelStatus     []struct {
		ID           string                    `json:"id"`
		Available    bool                      `json:"available"`
		LastDelivery *backupplan.AlertDelivery `json:"last_delivery"`
	} `json:"channel_status"`
}

// Keys & alerts: the settings offer the channels backup alerts can use, a
// PUT adds only usable ones, a test alert goes out through the same
// delivery, and an incident reaches the route beside the inbox.
func TestInstanceBackupAlertChannels(t *testing.T) {
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("c9", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.example.com")
	f := newInstanceFixture(t)
	var mu sync.Mutex
	var posts []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		posts = append(posts, m)
		mu.Unlock()
	}))
	defer hook.Close()
	ctx := context.Background()
	ch, err := notify.NewChannelStore(f.db).Create(ctx, notify.ChannelInput{WorkspaceID: "ws-new", Type: notify.ChannelWebhook, URL: hook.URL})
	if err != nil {
		t.Fatal(err)
	}

	// Instance admins only.
	wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/admin/instance/backups/settings/test-alert", `{"channel_id":"`+ch.ID+`"}`), http.StatusForbidden, "workspace admin test alert")

	got := decodeAs[alertSettingsView](t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/settings", "").Body.Bytes())
	if len(got.AvailableChannels) != 1 || got.AvailableChannels[0].ID != ch.ID || got.AvailableChannels[0].Kind != "webhook" ||
		!strings.HasSuffix(got.AvailableChannels[0].Name, " · ws-new") || len(got.ChannelStatus) != 0 {
		t.Fatalf("settings = %+v", got)
	}

	// The SSRF guard holds for a test alert: this receiver is on loopback.
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/settings/test-alert", `{"channel_id":"`+ch.ID+`"}`)
	wantCode(t, rr, http.StatusOK, "test alert to loopback")
	if res := decodeAs[backupplan.AlertTestResult](t, rr.Body.Bytes()); res.OK || res.Error == nil {
		t.Fatalf("a loopback receiver was reached: %+v", res)
	}
	t.Cleanup(notify.SetWebhookTransportForTesting(http.DefaultTransport))

	for _, c := range []struct {
		body string
		code int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"channel_id":"nch_nope"}`, http.StatusNotFound},
		{`{"channel_id":"` + ch.ID + `"}`, http.StatusOK},
	} {
		rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/settings/test-alert", c.body)
		wantCode(t, rr, c.code, "test alert "+c.body)
		if c.code == http.StatusOK {
			res := decodeAs[backupplan.AlertTestResult](t, rr.Body.Bytes())
			if !res.OK || res.ChannelID != ch.ID {
				t.Fatalf("test alert = %+v", res)
			}
		}
	}

	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings", `{"channels":["nch_nope"]}`), http.StatusBadRequest, "unknown channel")
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings", `{"channels":["`+ch.ID+`"]}`)
	wantCode(t, rr, http.StatusOK, "route the channel")
	if got := decodeAs[alertSettingsView](t, rr.Body.Bytes()); len(got.Channels) != 1 || len(got.ChannelStatus) != 1 || !got.ChannelStatus[0].Available || got.ChannelStatus[0].LastDelivery != nil {
		t.Fatalf("after put = %+v", got)
	}

	// A failed drill raises an incident: the inbox gets it, and so does the
	// channel, with the Overview link made absolute.
	f.r.backupPlans.RecordDrillOutcome(ctx, "", "failed", "the restore could not open attachments")
	if !waitForBackgroundWork(30 * time.Second) {
		t.Fatal("background work did not finish")
	}
	mu.Lock()
	last := posts[len(posts)-1]
	n := len(posts)
	mu.Unlock()
	if n != 2 || last["title"] != backupplan.AlertTitleRaised || last["url"] != "https://crewship.example.com"+backupplan.AlertLink ||
		!strings.Contains(last["body"].(string), "the restore could not open attachments") {
		t.Fatalf("channel got %d posts, last %v", n, last)
	}
	got = decodeAs[alertSettingsView](t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/settings", "").Body.Bytes())
	if d := got.ChannelStatus[0].LastDelivery; d == nil || d.Status != "sent" {
		t.Fatalf("channel status = %+v", got.ChannelStatus)
	}

	// A channel switched off after it was routed stays on the route (other
	// settings still save) and is reported unavailable.
	if _, err := f.db.Exec(`UPDATE notification_channels SET enabled = 0 WHERE id = ?`, ch.ID); err != nil {
		t.Fatal(err)
	}
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings", `{"stale_alert_hours":48}`)
	wantCode(t, rr, http.StatusOK, "save with an unavailable routed channel")
	if got := decodeAs[alertSettingsView](t, rr.Body.Bytes()); len(got.AvailableChannels) != 0 || len(got.ChannelStatus) != 1 || got.ChannelStatus[0].Available {
		t.Fatalf("after switching the channel off = %+v", got)
	}
}
