package backupplan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// Limits: how hard a backup may push the server.
type Limits struct {
	// Concurrency is how many runs execute at once; the rest queue.
	Concurrency int `json:"concurrency"`
	// CPUCores caps the zstd encoder's goroutines while packing.
	CPUCores int `json:"cpu_cores"`
	// DiskMBps caps writing staging and bundle files (0 = no cap).
	DiskMBps int `json:"disk_mbps"`
	// UploadMBps caps off-site uploads (0 = no cap).
	UploadMBps int `json:"upload_mbps"`
}

// AlertEvents says which incident kinds are delivered to instance admins.
// Incidents are recorded either way; a switched-off kind is not sent.
type AlertEvents struct {
	Failed     bool `json:"failed"`
	Incomplete bool `json:"incomplete"`
	Stale      bool `json:"stale"`
	Offsite    bool `json:"offsite"`
	Drill      bool `json:"drill"`
}

// Wants reports whether incidents of kind are delivered.
func (e AlertEvents) Wants(kind string) bool {
	switch kind {
	case IncidentFailed:
		return e.Failed
	case IncidentIncomplete:
		return e.Incomplete
	case IncidentStale:
		return e.Stale
	case IncidentOffsite:
		return e.Offsite
	case IncidentDrill:
		return e.Drill
	}
	return false
}

// Drill reminders.
const (
	DrillWeekly  = "weekly"
	DrillMonthly = "monthly"
	DrillOff     = "off"
)

// Settings is the single backup_settings row as the settings API speaks it
// (the console's BackupSettings minus the parts the API composes:
// destinations, instance_admins, local_path). Channels are notification
// channel ids (notification_channels.id) every alert also goes to, beside the
// instance admins' inboxes (alert_channels.go); the settings API checks the
// ones it adds.
type Settings struct {
	Limits             Limits      `json:"limits"`
	HeartbeatURL       *string     `json:"heartbeat_url"`
	HeartbeatLastAt    *string     `json:"heartbeat_last_at"`
	HeartbeatLastOK    *bool       `json:"heartbeat_last_ok"`
	HeartbeatLastError *string     `json:"heartbeat_last_error"`
	RecoveryKitEnabled bool        `json:"recovery_kit_enabled"`
	Channels           []string    `json:"channels"`
	Events             AlertEvents `json:"events"`
	StaleAlertHours    int         `json:"stale_alert_hours"`
	DrillReminder      string      `json:"drill_reminder"`
	UpdatedAt          *string     `json:"updated_at"`
}

// DefaultSettings is what a fresh install has (the migration's defaults).
func DefaultSettings() Settings {
	return Settings{
		Limits:          Limits{Concurrency: 1, CPUCores: 2},
		Channels:        []string{},
		Events:          AlertEvents{Failed: true, Incomplete: true, Stale: true, Offsite: true, Drill: true},
		StaleAlertHours: 36,
		DrillReminder:   DrillMonthly,
	}
}

// LoadSettings reads backup_settings. A missing row (or a schema without
// Track C's columns) reads as the defaults.
func LoadSettings(ctx context.Context, db *sql.DB) (Settings, error) {
	s := DefaultSettings()
	var hb, hbAt, hbStatus, updated sql.NullString
	var channels, events string
	var kit int
	err := db.QueryRowContext(ctx, `SELECT recovery_kit_enabled, concurrency, cpu_cores, disk_mbps, upload_mbps, heartbeat_url,
		heartbeat_last_at, heartbeat_last_status, alert_channels, alert_events, stale_alert_hours, drill_reminder, updated_at
		FROM backup_settings WHERE id = 1`).Scan(&kit, &s.Limits.Concurrency, &s.Limits.CPUCores, &s.Limits.DiskMBps,
		&s.Limits.UploadMBps, &hb, &hbAt, &hbStatus, &channels, &events, &s.StaleAlertHours, &s.DrillReminder, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such") {
			return DefaultSettings(), nil
		}
		return s, err
	}
	s.RecoveryKitEnabled = kit == 1
	s.HeartbeatURL, s.HeartbeatLastAt, s.UpdatedAt = strPtr(hb), strPtr(hbAt), strPtr(updated)
	if hbStatus.Valid {
		ok := hbStatus.String == "ok"
		s.HeartbeatLastOK = &ok
		if !ok {
			msg := hbStatus.String
			s.HeartbeatLastError = &msg
		}
	}
	s.Channels = decodeStrings(channels)
	_ = json.Unmarshal([]byte(events), &s.Events)
	return s, nil
}

// Normalize validates settings as sent to PUT …/backups/settings.
func (s *Settings) Normalize() error {
	l := &s.Limits
	if l.Concurrency < 1 || l.Concurrency > 16 {
		return invalid("limits.concurrency must be 1..16")
	}
	if l.CPUCores < 1 || l.CPUCores > 256 {
		return invalid("limits.cpu_cores must be 1..256")
	}
	if l.DiskMBps < 0 || l.UploadMBps < 0 {
		return invalid("limits.disk_mbps and limits.upload_mbps cannot be negative (0 is no limit)")
	}
	if s.HeartbeatURL != nil {
		raw := strings.TrimSpace(*s.HeartbeatURL)
		if raw == "" {
			s.HeartbeatURL = nil
		} else {
			if _, err := httpsafe.ValidateURL(raw, "https"); err != nil {
				return invalid("heartbeat_url must be a public https URL: %v", err)
			}
			s.HeartbeatURL = &raw
		}
	}
	if s.StaleAlertHours < 1 || s.StaleAlertHours > 24*90 {
		return invalid("stale_alert_hours must be 1..2160")
	}
	switch s.DrillReminder {
	case DrillWeekly, DrillMonthly, DrillOff:
	default:
		return invalid("drill_reminder must be weekly, monthly or off")
	}
	seen := map[string]bool{}
	chans := []string{}
	for _, c := range s.Channels {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if len(c) > 200 {
			return invalid("a channel id is at most 200 characters")
		}
		seen[c] = true
		chans = append(chans, c)
	}
	s.Channels = chans
	return nil
}

// SaveSettings writes every editable column of the row (the heartbeat's
// last-ping columns belong to the service and are left alone).
func SaveSettings(ctx context.Context, ex execer, s Settings, actor string, now time.Time) error {
	events, _ := json.Marshal(s.Events)
	_, err := ex.ExecContext(ctx, `INSERT INTO backup_settings (id, recovery_kit_enabled, concurrency, cpu_cores, disk_mbps, upload_mbps,
		heartbeat_url, alert_channels, alert_events, stale_alert_hours, drill_reminder, updated_at, updated_by)
		VALUES (1,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET recovery_kit_enabled=excluded.recovery_kit_enabled, concurrency=excluded.concurrency,
		cpu_cores=excluded.cpu_cores, disk_mbps=excluded.disk_mbps, upload_mbps=excluded.upload_mbps,
		heartbeat_url=excluded.heartbeat_url, alert_channels=excluded.alert_channels, alert_events=excluded.alert_events,
		stale_alert_hours=excluded.stale_alert_hours, drill_reminder=excluded.drill_reminder,
		updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
		boolInt(s.RecoveryKitEnabled), s.Limits.Concurrency, s.Limits.CPUCores, s.Limits.DiskMBps, s.Limits.UploadMBps,
		nullStrPtr(s.HeartbeatURL), mustJSON(s.Channels), string(events), s.StaleAlertHours, s.DrillReminder,
		ts(now), nullStr(actor))
	return err
}

// recordHeartbeat stores the last ping's time and outcome ("ok" or the error).
func recordHeartbeat(ctx context.Context, db *sql.DB, at time.Time, status string) error {
	if len(status) > 500 {
		status = status[:500]
	}
	_, err := db.ExecContext(ctx, `UPDATE backup_settings SET heartbeat_last_at = ?, heartbeat_last_status = ? WHERE id = 1`, ts(at), status)
	return err
}

// SettingsConcurrency reads limits.concurrency for the service's
// Concurrency hook (the default when unreadable).
func SettingsConcurrency(db *sql.DB) func(ctx context.Context) int {
	return func(ctx context.Context) int {
		s, err := LoadSettings(ctx, db)
		if err != nil {
			return DefaultConcurrency
		}
		return s.Limits.Concurrency
	}
}

// limiterSet keeps ONE shared limiter per kind (disk, upload), so the cap is
// instance-wide however many runs or uploads are in flight. A limiter is
// rebuilt only when its rate changes.
type limiterSet struct {
	mu               sync.Mutex
	diskRate, upRate int
	disk, upload     *offsite.Limiter
	clock            offsite.Clock
}

const mb = 1 << 20

func (l *limiterSet) get(lim Limits) (disk, upload *offsite.Limiter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lim.DiskMBps != l.diskRate || (l.disk == nil) != (lim.DiskMBps <= 0) {
		l.diskRate, l.disk = lim.DiskMBps, offsite.NewLimiter(int64(lim.DiskMBps)*mb, l.clock)
	}
	if lim.UploadMBps != l.upRate || (l.upload == nil) != (lim.UploadMBps <= 0) {
		l.upRate, l.upload = lim.UploadMBps, offsite.NewLimiter(int64(lim.UploadMBps)*mb, l.clock)
	}
	return l.disk, l.upload
}
