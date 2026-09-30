package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

// Until standalone service snapshot transport is installed, never publish a
// bundle whose crew declaration promises data outside the collected filesystem.
// Check both admission and the immutable dump: declarations can change in between.
func requireSupportedServiceBackups(ctx context.Context, db *sql.DB, crews []CrewTarget) error {
	for _, crew := range crews {
		var raw string
		if err := db.QueryRowContext(ctx, "SELECT COALESCE(services_json, '') FROM crews WHERE id = ?", crew.ID).Scan(&raw); err != nil {
			return errors.New("backup: cannot read service configuration")
		}
		if err := requireSupportedServiceConfig(raw); err != nil {
			return err
		}
	}
	return nil
}

func requireSupportedDumpServiceBackups(dump *DBDump) error {
	for _, row := range dump.Tables["crews"] {
		var raw string
		switch value := row["services_json"].(type) {
		case nil:
		case string:
			raw = value
		case []byte:
			raw = string(value)
		default:
			return errors.New("backup: invalid service configuration")
		}
		if err := requireSupportedServiceConfig(raw); err != nil {
			return err
		}
	}
	return nil
}

func requireSupportedServiceConfig(raw string) error {
	plain, err := serviceconfig.Open(raw)
	if err != nil {
		return errors.New("backup: cannot decrypt service configuration")
	}
	if strings.TrimSpace(plain) == "" {
		return nil
	}
	var services []struct {
		QuotaEnforced bool              `json:"quota_enforced"`
		Volumes       []json.RawMessage `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(plain), &services); err != nil {
		return errors.New("backup: invalid service configuration")
	}
	for _, service := range services {
		if service.QuotaEnforced && len(service.Volumes) != 0 {
			return errors.New("backup: standalone quota service data backup unsupported until snapshot transport is configured")
		}
	}
	return nil
}
