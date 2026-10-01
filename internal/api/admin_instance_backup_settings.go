package api

// Admin › Backups: settings, backup keys, off-site destinations, incidents
// and the recovery sheet (Track C2). Instance admins only (authedInstance).
//
//	GET    /api/v1/admin/instance/backups/settings                 limits, heartbeat, alerts, destinations, instance admins
//	PUT    /api/v1/admin/instance/backups/settings                 change (fields not sent keep their value)
//	POST   /api/v1/admin/instance/backups/settings/test-alert      {channel_id}: send a test alert to one channel now
//	GET    /api/v1/admin/instance/backups/recipients               backup keys (age public keys)
//	POST   /api/v1/admin/instance/backups/recipients               {name, public_key, holder}
//	DELETE /api/v1/admin/instance/backups/recipients/{id}          refused while a plan encrypts to it
//	GET    /api/v1/admin/instance/backups/destinations             off-site destinations with their verified copies
//	POST   /api/v1/admin/instance/backups/destinations             add an S3-compatible store (connection tested first)
//	POST   /api/v1/admin/instance/backups/destinations/{id}/test   test the connection again
//	DELETE /api/v1/admin/instance/backups/destinations/{id}        refused while a plan copies to it
//	GET    /api/v1/admin/instance/backups/incidents                ?state=open|resolved&limit=
//	GET    /api/v1/admin/instance/backups/recovery-sheet           text/markdown: what a recovery on a new server needs
//
// Every change writes its instance audit entry in the transaction that makes
// it. A destination's secret access key is sealed with the vault key
// (encryption.Encrypt) and never returned, logged or audited.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// backupDestinationView is one entry of BackupSettings.destinations: the
// console's Destination (local, each S3 store, and Drive as "later").
type backupDestinationView struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"` // local | s3 | drive
	Label     string  `json:"label"`
	Path      *string `json:"path"`
	UsedBytes *int64  `json:"used_bytes"`
	// Verified: at least one upload here was checked at the destination.
	Verified bool `json:"verified"`
	// Available: this server can use the kind (false: shown as later).
	Available bool `json:"available"`
}

// backupSettingsResponse is GET/PUT …/backups/settings (BackupSettings).
type backupSettingsResponse struct {
	backupplan.Settings
	Destinations   []backupDestinationView `json:"destinations"`
	InstanceAdmins int                     `json:"instance_admins"`
	LocalPath      *string                 `json:"local_path"`
	// AvailableChannels are the notification channels that can carry
	// backup alerts now (the picker); channels holds the chosen ids.
	AvailableChannels []backupplan.AlertChannel `json:"available_channels"`
	// ChannelStatus is the newest backup alert's outcome per chosen channel,
	// including one that is no longer available (it then says why it fails).
	ChannelStatus []backupAlertRouteEntry `json:"channel_status"`
}

// backupAlertRouteEntry is one chosen channel and how its alerts are going.
type backupAlertRouteEntry struct {
	ID           string                    `json:"id"`
	Available    bool                      `json:"available"`
	LastDelivery *backupplan.AlertDelivery `json:"last_delivery"`
}

func (p *InstanceBackupPlansHandler) settingsResponse(ctx context.Context) (*backupSettingsResponse, error) {
	set, err := backupplan.LoadSettings(ctx, p.h.db)
	if err != nil {
		return nil, err
	}
	out := &backupSettingsResponse{Settings: set, Destinations: []backupDestinationView{}}
	dir := ""
	if p.backupsDir != nil {
		dir, _ = p.backupsDir()
	}
	if dir != "" {
		out.LocalPath = &dir
	}
	var used int64
	if cat, err := backup.ListCatalog(ctx, p.h.db, ""); err == nil {
		for _, e := range cat {
			used += e.Size
		}
	}
	out.Destinations = append(out.Destinations, backupDestinationView{
		ID: backupplan.DestinationLocal, Kind: "local", Label: "This server", Path: out.LocalPath, UsedBytes: &used,
		Verified: true, Available: true,
	})
	dests, err := backupplan.ListDestinations(ctx, p.h.db)
	if err != nil {
		return nil, err
	}
	for _, d := range dests {
		bytes := d.CopyBytes
		out.Destinations = append(out.Destinations, backupDestinationView{
			ID: d.ID, Kind: d.Kind, Label: d.Name, UsedBytes: &bytes, Verified: d.Copies > 0, Available: true,
		})
	}
	out.Destinations = append(out.Destinations, backupDestinationView{ID: "drive", Kind: "drive", Label: "Google Drive"})
	admins, err := listInstanceAdmins(ctx, p.h.db)
	if err != nil {
		return nil, err
	}
	out.InstanceAdmins = len(admins)
	if out.AvailableChannels, err = backupplan.ListAlertChannels(ctx, p.h.db); err != nil {
		return nil, err
	}
	avail := map[string]bool{}
	for _, c := range out.AvailableChannels {
		avail[c.ID] = true
	}
	out.ChannelStatus = []backupAlertRouteEntry{}
	for _, id := range set.Channels {
		out.ChannelStatus = append(out.ChannelStatus, backupAlertRouteEntry{ID: id, Available: avail[id], LastDelivery: backupplan.LastAlertDelivery(ctx, p.h.db, id)})
	}
	return out, nil
}

// GetSettings is GET …/backups/settings.
func (p *InstanceBackupPlansHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	out, err := p.settingsResponse(r.Context())
	if err != nil {
		p.h.fail(w, "backup settings", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PutSettings is PUT …/backups/settings. The body is a partial BackupSettings;
// what it leaves out keeps its value, and the composed parts (destinations,
// instance_admins, local_path, the heartbeat's last ping) are ignored.
func (p *InstanceBackupPlansHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	before, err := backupplan.LoadSettings(ctx, p.h.db)
	if err != nil {
		p.h.fail(w, "backup settings", err)
		return
	}
	body, err := readBody(r)
	if err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	next := before
	next.Channels = append([]string(nil), before.Channels...)
	if err := json.Unmarshal(body, &next); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := next.Normalize(); err != nil {
		p.planError(w, "backup settings", err)
		return
	}
	if err := backupplan.ValidateAlertChannels(ctx, p.h.db, next.Channels, before.Channels); err != nil {
		p.planError(w, "backup settings", err)
		return
	}
	userID, _ := actorUserID(r)
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "backup settings", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := backupplan.SaveSettings(ctx, tx, next, userID, p.now()); err != nil {
		p.h.fail(w, "save backup settings", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_settings_updated", "backup_settings", "settings", "", map[string]any{
		"before": settingsAuditMeta(before), "after": settingsAuditMeta(next),
	}); err != nil {
		p.h.fail(w, "audit backup settings", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "backup settings", err)
		return
	}
	p.svc.Kick() // a raised concurrency limit may start queued runs now
	out, err := p.settingsResponse(ctx)
	if err != nil {
		p.h.fail(w, "backup settings", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// testAlertRequest is POST …/backups/settings/test-alert.
type testAlertRequest struct {
	ChannelID string `json:"channel_id"`
}

// TestAlert is POST …/backups/settings/test-alert {channel_id}: sends a test
// alert to one notification channel now, through the same delivery as a real
// one, and says whether it arrived (200 with ok false and the error when it
// did not). The channel need not be on the route yet; it must be one the
// picker offers (404 for an unknown or personal channel, 400 for one that
// cannot carry backup alerts, with the reason).
func (p *InstanceBackupPlansHandler) TestAlert(w http.ResponseWriter, r *http.Request) {
	var req testAlertRequest
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	req.ChannelID = strings.TrimSpace(req.ChannelID)
	if req.ChannelID == "" {
		replyError(w, http.StatusBadRequest, "channel_id is required")
		return
	}
	if p.alerts == nil {
		p.h.fail(w, "test alert", errors.New("backup alerts are not wired on this server"))
		return
	}
	res, err := p.alerts.Test(r.Context(), req.ChannelID)
	if errors.Is(err, backupplan.ErrNotFound) {
		replyError(w, http.StatusNotFound, "no notification channel with that id (see available_channels in GET …/backups/settings)")
		return
	}
	if err != nil {
		p.planError(w, "test alert", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func settingsAuditMeta(s backupplan.Settings) map[string]any {
	return map[string]any{
		"limits": s.Limits, "heartbeat_url_set": s.HeartbeatURL != nil, "recovery_kit_enabled": s.RecoveryKitEnabled,
		"channels": s.Channels, "events": s.Events, "stale_alert_hours": s.StaleAlertHours, "drill_reminder": s.DrillReminder,
	}
}

// ── Backup keys ─────────────────────────────────────────────────────────────

// ListRecipients is GET …/backups/recipients.
func (p *InstanceBackupPlansHandler) ListRecipients(w http.ResponseWriter, r *http.Request) {
	list, err := backupplan.ListRecipients(r.Context(), p.h.db)
	if err != nil {
		p.h.fail(w, "list backup keys", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// CreateRecipient is POST …/backups/recipients {name, public_key, holder}.
func (p *InstanceBackupPlansHandler) CreateRecipient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var rec backupplan.Recipient
	if !decodeInstanceBody(w, r, &rec) {
		return
	}
	if err := backupplan.NormalizeRecipient(&rec); err != nil {
		p.planError(w, "add backup key", err)
		return
	}
	userID, _ := actorUserID(r)
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "add backup key", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := backupplan.InsertRecipient(ctx, tx, &rec, userID, p.now()); err != nil {
		if errors.Is(err, backupplan.ErrDuplicateRecipient) {
			replyError(w, http.StatusConflict, "that public key is already a backup key")
			return
		}
		p.h.fail(w, "add backup key", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_recipient_added", "backup_recipient", rec.ID, "", map[string]any{
		"name": rec.Name, "holder": rec.Holder, "fingerprint": rec.Fingerprint,
	}); err != nil {
		p.h.fail(w, "audit backup key", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "add backup key", err)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

// DeleteRecipient is DELETE …/backups/recipients/{id}.
func (p *InstanceBackupPlansHandler) DeleteRecipient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	rec, err := backupplan.GetRecipient(ctx, p.h.db, id)
	if errors.Is(err, backupplan.ErrNotFound) {
		replyError(w, http.StatusNotFound, "no backup key with that id")
		return
	}
	if err != nil {
		p.h.fail(w, "get backup key", err)
		return
	}
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "remove backup key", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	used, err := backupplan.DeleteRecipient(ctx, p.h.db, tx, id)
	if errors.Is(err, backupplan.ErrRecipientInUse) {
		replyError(w, http.StatusConflict, fmt.Sprintf("the plan(s) %s still encrypt to this key; change them first", strings.Join(used, ", ")))
		return
	}
	if err != nil {
		p.h.fail(w, "remove backup key", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_recipient_removed", "backup_recipient", rec.ID, "", map[string]any{
		"name": rec.Name, "fingerprint": rec.Fingerprint,
	}); err != nil {
		p.h.fail(w, "audit backup key", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "remove backup key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Off-site destinations ───────────────────────────────────────────────────

// createDestinationRequest is POST …/backups/destinations.
type createDestinationRequest struct {
	Name                string `json:"name"`
	Kind                string `json:"kind"`
	Endpoint            string `json:"endpoint"`
	Region              string `json:"region"`
	Bucket              string `json:"bucket"`
	Prefix              string `json:"prefix"`
	AccessKeyID         string `json:"access_key_id"`
	SecretAccessKey     string `json:"secret_access_key"`
	PathStyle           bool   `json:"path_style"`
	AllowPrivateNetwork bool   `json:"allow_private_network"`
	// SkipTest stores the destination without the connection test (it is
	// then tested by the first upload).
	SkipTest bool `json:"skip_test"`
}

type destinationTestResult struct {
	OK       bool    `json:"ok"`
	Error    *string `json:"error"`
	TestedAt string  `json:"tested_at"`
}

type createDestinationResponse struct {
	Destination backupplan.Destination `json:"destination"`
	Test        *destinationTestResult `json:"test"`
	Warning     *string                `json:"warning"`
}

// privateNetworkWarning is returned whenever a destination may reach private
// addresses.
const privateNetworkWarning = "allow_private_network is on: this destination may reach loopback and private (LAN) addresses, and plain http. Cloud metadata and link-local addresses stay blocked."

// newDestinationClient builds the transfer client for a destination being
// added or tested (a seam for tests).
var newDestinationClient = func(cfg offsite.S3Config) (offsite.Destination, error) { return offsite.NewS3(cfg) }

// ListDestinations is GET …/backups/destinations.
func (p *InstanceBackupPlansHandler) ListDestinations(w http.ResponseWriter, r *http.Request) {
	list, err := backupplan.ListDestinations(r.Context(), p.h.db)
	if err != nil {
		p.h.fail(w, "list destinations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// CreateDestination is POST …/backups/destinations: validate, test the
// connection (put, head and delete one small object), seal the secret, store.
// A failed test is a 422 and nothing is stored.
func (p *InstanceBackupPlansHandler) CreateDestination(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req createDestinationRequest
	if !decodeInstanceBody(w, r, &req) {
		return
	}
	d := backupplan.Destination{
		Name: req.Name, Kind: req.Kind, Endpoint: req.Endpoint, Region: req.Region, Bucket: req.Bucket, Prefix: req.Prefix,
		AccessKeyID: req.AccessKeyID, PathStyle: req.PathStyle, AllowPrivateNetwork: req.AllowPrivateNetwork,
	}
	secret := strings.TrimSpace(req.SecretAccessKey)
	if err := backupplan.NormalizeDestination(&d, secret); err != nil {
		p.planError(w, "add destination", err)
		return
	}
	resp := createDestinationResponse{}
	if d.AllowPrivateNetwork {
		msg := privateNetworkWarning
		resp.Warning = &msg
	}
	now := p.now()
	if !req.SkipTest {
		client, err := newDestinationClient(d.S3Config(secret))
		if err == nil {
			tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			err = client.Test(tctx)
			cancel()
		}
		at := now.Format(time.RFC3339)
		resp.Test = &destinationTestResult{OK: err == nil, TestedAt: at}
		if err != nil {
			msg := err.Error()
			resp.Test.Error = &msg
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "the connection test failed: " + msg, "test": resp.Test, "warning": resp.Warning})
			return
		}
		d.LastTestAt = &at
	}
	sealed, err := encryption.Encrypt(secret)
	if err != nil {
		replyError(w, http.StatusConflict, "the secret cannot be stored: this server has no vault key (ENCRYPTION_KEY) to seal it with")
		return
	}
	userID, _ := actorUserID(r)
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "add destination", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := backupplan.InsertDestination(ctx, tx, &d, sealed, userID, now); err != nil {
		p.h.fail(w, "add destination", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_destination_added", "backup_destination", d.ID, "", map[string]any{
		"name": d.Name, "kind": d.Kind, "endpoint": d.Endpoint, "bucket": d.Bucket, "prefix": d.Prefix,
		"allow_private_network": d.AllowPrivateNetwork, "tested": !req.SkipTest,
	}); err != nil {
		p.h.fail(w, "audit destination", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "add destination", err)
		return
	}
	resp.Destination = d
	writeJSON(w, http.StatusCreated, resp)
}

// TestDestination is POST …/backups/destinations/{id}/test.
func (p *InstanceBackupPlansHandler) TestDestination(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := backupplan.GetDestination(ctx, p.h.db, id); err != nil {
		if errors.Is(err, backupplan.ErrNotFound) {
			replyError(w, http.StatusNotFound, "no backup destination with that id")
			return
		}
		p.h.fail(w, "get destination", err)
		return
	}
	open := p.svc.Destinations
	if open == nil {
		open = backupplan.DBDestinations(p.h.db)
	}
	client, _, err := open(ctx, id)
	if err == nil {
		tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err = client.Test(tctx)
		cancel()
	}
	now := p.now()
	if rerr := backupplan.RecordDestinationTest(ctx, p.h.db, id, now, err); rerr != nil {
		p.h.logger.Warn("instance backups: record destination test", "error", rerr)
	}
	res := destinationTestResult{OK: err == nil, TestedAt: now.Format(time.RFC3339)}
	if err != nil {
		msg := err.Error()
		res.Error = &msg
	}
	writeJSON(w, http.StatusOK, res)
}

// DeleteDestination is DELETE …/backups/destinations/{id}.
func (p *InstanceBackupPlansHandler) DeleteDestination(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	d, err := backupplan.GetDestination(ctx, p.h.db, id)
	if errors.Is(err, backupplan.ErrNotFound) {
		replyError(w, http.StatusNotFound, "no backup destination with that id")
		return
	}
	if err != nil {
		p.h.fail(w, "get destination", err)
		return
	}
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "remove destination", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	used, err := backupplan.DeleteDestination(ctx, p.h.db, tx, id)
	if errors.Is(err, backupplan.ErrDestinationInUse) {
		replyError(w, http.StatusConflict, fmt.Sprintf("the plan(s) %s still copy to this destination; change them first", strings.Join(used, ", ")))
		return
	}
	if err != nil {
		p.h.fail(w, "remove destination", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_destination_removed", "backup_destination", d.ID, "", map[string]any{
		"name": d.Name, "endpoint": d.Endpoint, "bucket": d.Bucket,
	}); err != nil {
		p.h.fail(w, "audit destination", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "remove destination", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Incidents ───────────────────────────────────────────────────────────────

// ListIncidents is GET …/backups/incidents?state=open|resolved&limit= (default 100, max 500).
func (p *InstanceBackupPlansHandler) ListIncidents(w http.ResponseWriter, r *http.Request) {
	f := backupplan.IncidentFilter{Limit: 100, State: r.URL.Query().Get("state")}
	switch f.State {
	case "", "open", "resolved":
	default:
		replyError(w, http.StatusBadRequest, "state must be open or resolved")
		return
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			replyError(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
		f.Limit = n
	}
	list, err := backupplan.ListIncidents(r.Context(), p.h.db, f)
	if err != nil {
		p.h.fail(w, "list incidents", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// ── Recovery sheet ──────────────────────────────────────────────────────────

// RecoverySheet is GET …/backups/recovery-sheet: a Markdown page to print or
// keep off this server — where the bundles are, which keys open them (public
// halves and fingerprints only), whether the vault keys ride along, the exact
// recover and drill commands, and who administers the instance.
func (p *InstanceBackupPlansHandler) RecoverySheet(w http.ResponseWriter, r *http.Request) {
	sheet, err := p.recoverySheet(r.Context())
	if err != nil {
		p.h.fail(w, "recovery sheet", err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="crewship-recovery-sheet.md"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sheet))
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

func (p *InstanceBackupPlansHandler) recoverySheet(ctx context.Context) (string, error) {
	db := p.h.db
	now := p.now()
	set, err := backupplan.LoadSettings(ctx, db)
	if err != nil {
		return "", err
	}
	recipients, err := backupplan.ListRecipients(ctx, db)
	if err != nil {
		return "", err
	}
	dests, err := backupplan.ListDestinations(ctx, db)
	if err != nil {
		return "", err
	}
	plans, err := backupplan.ListPlans(ctx, db)
	if err != nil {
		return "", err
	}
	admins, err := listInstanceAdmins(ctx, db)
	if err != nil {
		return "", err
	}
	cat, err := backup.ListCatalog(ctx, db, "")
	if err != nil {
		return "", err
	}
	dir := ""
	if p.backupsDir != nil {
		dir, _ = p.backupsDir()
	}
	var newest *backup.CatalogEntry
	for i := range cat {
		e := &cat[i]
		if e.Scope == string(backup.ScopeInstance) && e.Kind != backup.KindCustom && (newest == nil || e.CreatedAt.After(newest.CreatedAt)) {
			newest = e
		}
	}
	var b strings.Builder
	host := backup.CurrentInstanceHostname(ctx, db)
	if host == "" {
		host, _ = os.Hostname()
	}
	fmt.Fprintf(&b, "# Crewship recovery sheet\n\n")
	fmt.Fprintf(&b, "Instance **%s** · generated %s UTC", host, now.UTC().Format("2006-01-02 15:04"))
	if v := os.Getenv("CREWSHIP_VERSION"); v != "" {
		fmt.Fprintf(&b, " · Crewship %s", v)
	}
	b.WriteString("\n\nKeep this page off the server it describes. It holds no secret: no private key, no vault key, no password.\n\n")

	b.WriteString("## Where the backups are\n\n")
	if dir == "" {
		dir = "the server's backups directory ($CREWSHIP_DATA_DIR/backups)"
	}
	fmt.Fprintf(&b, "- **This server:** `%s`\n", dir)
	if newest != nil {
		fmt.Fprintf(&b, "  - newest instance backup: `%s` (%s UTC)\n", filepath.Base(newest.FilePath), newest.CreatedAt.UTC().Format("2006-01-02 15:04"))
	} else {
		b.WriteString("  - no instance backup exists yet\n")
	}
	if len(dests) == 0 {
		b.WriteString("- **Off-site:** none. Losing this server loses every backup with it.\n")
	}
	for _, d := range dests {
		loc := strings.TrimRight(d.Endpoint, "/") + "/" + d.Bucket
		if d.Prefix != "" {
			loc += "/" + d.Prefix
		}
		fmt.Fprintf(&b, "- **Off-site · %s:** S3-compatible, `%s` (region %s, access key id `%s`). %d verified copies",
			d.Name, loc, orDash(d.Region), d.AccessKeyID, d.Copies)
		if d.LastVerifiedAt != nil {
			fmt.Fprintf(&b, ", newest checked %s", *d.LastVerifiedAt)
		}
		b.WriteString(". Instance bundles are under `instance/`, workspace bundles under `workspaces/<workspace id>/`. The secret access key is not on this sheet.\n")
	}

	b.WriteString("\n## Keys that open a backup (age)\n\n")
	if len(recipients) == 0 {
		b.WriteString("No backup key is stored on this server. Plans may name age public keys directly; see the plans below.\n")
	} else {
		b.WriteString("Every backup is encrypted to these public keys. The private halves are NOT on this server; whoever holds one can open every backup made to it.\n\n")
		b.WriteString("| Name | Held by | Fingerprint | Public key |\n|---|---|---|---|\n")
		for _, rc := range recipients {
			fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` |\n", mdCell(rc.Name), mdCell(orDash(rc.Holder)), rc.Fingerprint, rc.PublicKey)
		}
	}

	b.WriteString("\n## Keys that open the secrets inside (vault)\n\n")
	if set.RecoveryKitEnabled {
		b.WriteString("The **recovery kit is on**: every vault key version rides inside instance backups, encrypted to the keys above. A recovery on a new server unlocks credentials, webhook secrets and integration keys without re-entering them.\n")
	} else {
		b.WriteString("The **recovery kit is off**: instance backups do not carry the vault keys. Keep `ENCRYPTION_KEY` (and every `ENCRYPTION_KEY_V<N>`) somewhere safe, or every credential must be re-entered after a recovery.\n")
	}

	b.WriteString("\n## Recover the whole instance on a new server\n\n")
	bundle := "<newest crewship-instance-all-….tar.zst>"
	if newest != nil {
		bundle = filepath.Base(newest.FilePath)
	}
	b.WriteString("1. Install Crewship, do not start it.\n2. Copy the newest instance bundle to the new server")
	if len(dests) > 0 {
		b.WriteString(" (from this server, or from the off-site bucket above)")
	}
	b.WriteString(".\n3. Run, with the private half of one key above:\n\n")
	fmt.Fprintf(&b, "```\ncrewship recover --bundle %s --identity /path/to/backup-key.txt --data-dir /var/lib/crewship\n```\n\n", bundle)
	b.WriteString("4. Start the server. Routines, webhooks and queued work stay held until an instance admin resumes them (`crewship admin instance holds resume all`).\n")

	b.WriteString("\n## Prove it (drill)\n\n")
	b.WriteString("Restore into an isolated instance, check it, and record the result on the server:\n\n")
	fmt.Fprintf(&b, "```\ncrewship backup drill --bundle %s --identity /path/to/backup-key.txt --post\n```\n\n", bundle)
	fmt.Fprintf(&b, "Drill reminder: %s.\n", set.DrillReminder)

	b.WriteString("\n## Plans\n\n")
	if len(plans) == 0 {
		b.WriteString("No backup plan exists.\n")
	}
	for _, pl := range plans {
		state := "enabled"
		if !pl.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&b, "- **%s** (%s, %s): %s %s at %s %s; keeps at least %d, %d daily, %d weekly, %d monthly; to %s.\n",
			pl.Name, pl.Preset, state, pl.Scope, pl.Cadence, pl.TimeOfDay, pl.Timezone, pl.KeepMin, pl.KeepDaily, pl.KeepWeekly,
			pl.KeepMonthly, strings.Join(pl.Destinations, ", "))
	}

	b.WriteString("\n## Instance admins\n\n")
	sort.Slice(admins, func(i, j int) bool { return admins[i].Email < admins[j].Email })
	for _, a := range admins {
		name := a.Name
		if name == "" {
			name = a.Email
		}
		fmt.Fprintf(&b, "- %s <%s>\n", name, a.Email)
	}
	if len(admins) == 0 {
		b.WriteString("- none named\n")
	}
	return b.String(), nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
