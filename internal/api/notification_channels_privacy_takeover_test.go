package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/notify"
)

func TestNotifyChannelHandler_ListWebhookURLAuthority(t *testing.T) {
	db := newNotifyChannelDB(t)
	defer db.Close()
	h := NewNotifyChannelHandler(db, mailer.Disabled{}, slog.Default())
	destination := "https://qa-user:qa-password@hooks.example.test/private-path?token=private-query"
	response := postChannel(t, h, "ws1", map[string]string{"type": "webhook", "url": destination})
	if response.Code != http.StatusCreated {
		t.Fatalf("create %d %s", response.Code, response.Body)
	}
	var created struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	personal, err := h.store.Create(t.Context(), notify.ChannelInput{WorkspaceID: "ws1", Type: notify.ChannelWebhook, URL: destination, Scope: notify.ScopeUser, OwnerUserID: "viewer", CreatedBy: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role, user, query           string
		wantWorkspace, wantPersonal bool
	}{
		{"VIEWER", "viewer", "", false, true},
		{"MEMBER", "viewer", "?scope=all", false, true},
		{"MANAGER", "viewer", "", false, true},
		{"ADMIN", "admin", "", true, false},
		{"OWNER", "admin", "?scope=all", true, false},
	} {
		t.Run(tc.role+tc.query, func(t *testing.T) {
			request := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-channels"+tc.query, nil), tc.user, "ws1", tc.role)
			response := httptest.NewRecorder()
			h.List(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("list %d %s", response.Code, response.Body)
			}
			var listed struct {
				Channels []notify.Channel `json:"channels"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
				t.Fatal(err)
			}
			foundWorkspace, foundPersonal := false, false
			for _, channel := range listed.Channels {
				switch channel.ID {
				case created.ID:
					foundWorkspace = true
					if tc.wantWorkspace && channel.URL != destination {
						t.Fatal("manager lost destination")
					}
					if !tc.wantWorkspace && channel.URL != "" {
						t.Fatal("workspace destination visible without management authority")
					}
					if channel.Type != notify.ChannelWebhook || channel.WorkspaceID != "ws1" {
						t.Fatal("safe channel metadata lost")
					}
				case personal.ID:
					foundPersonal = true
					if tc.wantPersonal && channel.URL != destination {
						t.Fatal("owner lost personal destination")
					}
					if !tc.wantPersonal && channel.URL != "" {
						t.Fatal("other personal destination visible")
					}
				}
			}
			if !foundWorkspace || tc.wantPersonal && !foundPersonal {
				t.Fatal("expected channel missing")
			}
			if created.Secret == "" || strings.Contains(response.Body.String(), created.Secret) {
				t.Fatal("signing secret visible")
			}
		})
	}
	// API redaction must not alter delivery configuration or the signing secret.
	dispatch, err := h.store.GetForDispatch(t.Context(), "ws1", created.ID)
	if err != nil || dispatch.URL != destination || dispatch.Secret != created.Secret {
		t.Fatal("delivery configuration changed", err)
	}
}
