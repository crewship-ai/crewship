package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/notify"
)

// NotifyProvidersHandler serves the shoutrrr providers registry (#1412):
// which URL-scheme providers this Crewship instance SUPPORTS (a fixed,
// code-level list — notify.SupportedProviders) and which are
// admin-ENABLED (a per-instance app_settings toggle, default enabled). A
// disabled provider still lets existing channels using it keep working
// (this is a create-time gate, not a kill switch) — mirrors how a
// deleted/unconfigured mailer degrades existing email sends to a logged
// no-op rather than breaking history.
type NotifyProvidersHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewNotifyProvidersHandler(db *sql.DB, logger *slog.Logger) *NotifyProvidersHandler {
	return &NotifyProvidersHandler{db: db, logger: logger}
}

// providerSettingKey returns the app_settings key gating provider p.
// Namespaced under "notify.provider." so a future admin-settings sweep
// doesn't collide with the pre-existing telemetry keys in that table.
func providerSettingKey(p string) string {
	return "notify.provider." + p + ".enabled"
}

// providerInfo describes one provider to a client. It carries the FORM
// DEFINITION — label, blurb and per-field label/help/placeholder — so the UI
// and the CLI render the same questions without either of them hard-coding a
// provider list that drifts from the server's.
//
// `scheme` is retained for API compatibility and mirrors `provider`. The
// delivery library's URL scheme is an implementation detail a client has no
// use for: clients send field values, the server composes the URL.
type providerInfo struct {
	Provider string `json:"provider"`
	Scheme   string `json:"scheme"`
	Label    string `json:"label"`
	Blurb    string `json:"blurb"`
	// Category is the catalog section (chat | push | incident). Served rather
	// than mapped client-side so a new provider lands in the right section
	// without a matching frontend change — see notify.ProviderCategories.
	Category string                 `json:"category"`
	Fields   []notify.ProviderField `json:"fields"`
	Enabled  bool                   `json:"enabled"`
}

// List serves GET /api/v1/notification-providers.
func (h *NotifyProvidersHandler) List(w http.ResponseWriter, r *http.Request) {
	specs := notify.Providers()
	out := make([]providerInfo, 0, len(specs))
	for _, spec := range specs {
		enabled, err := providerEnabled(r.Context(), h.db, spec.Name)
		if err != nil {
			h.logger.Error("notify: read provider setting", "err", err, "provider", spec.Name)
			replyError(w, http.StatusInternalServerError, "internal")
			return
		}
		out = append(out, providerInfo{
			Provider: spec.Name,
			Scheme:   spec.Name,
			Label:    spec.Label,
			Blurb:    spec.Blurb,
			Category: string(spec.Category),
			Fields:   spec.Fields,
			Enabled:  enabled,
		})
	}
	// `categories` ships alongside so a client renders sections in OUR order
	// with OUR labels instead of inferring them from the providers it happens
	// to have received.
	writeJSON(w, http.StatusOK, map[string]any{
		"providers":  out,
		"categories": notify.ProviderCategories(),
	})
}

// patchProviderRequest is the PATCH body.
type patchProviderRequest struct {
	Enabled bool `json:"enabled"`
}

// Patch serves PATCH /api/v1/notification-providers/{provider}. ADMIN/OWNER
// only (roleManage, enforced by the route table).
func (h *NotifyProvidersHandler) Patch(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	known := false
	for _, p := range notify.SupportedProviders() {
		if p == provider {
			known = true
			break
		}
	}
	if !known {
		replyError(w, http.StatusNotFound, fmt.Sprintf("unknown provider %q", provider))
		return
	}
	var body patchProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO app_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		providerSettingKey(provider), value); err != nil {
		h.logger.Error("notify: write provider setting", "err", err, "provider", provider)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": provider, "enabled": body.Enabled})
}

// providerEnabled reads the app_settings toggle for provider p, defaulting
// to true (enabled) when no row exists — a freshly-upgraded instance
// doesn't need an admin to opt every provider back in. Shared with
// NotifyChannelHandler.Create, which fails closed on a disabled provider.
func providerEnabled(ctx context.Context, db *sql.DB, p string) (bool, error) {
	var value string
	err := db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, providerSettingKey(p)).Scan(&value)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return value == "true", nil
}
