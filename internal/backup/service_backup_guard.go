package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

// Never publish a bundle whose crew declaration promises standalone data without
// the host snapshot transport and an exact verified image for every declaration.
// Check both admission and the immutable dump: declarations can change in between.
func requireSupportedServiceBackups(ctx context.Context, db *sql.DB, crews []CrewTarget, runtime ServiceSnapshotRuntime) error {
	for _, crew := range crews {
		var raw string
		if err := db.QueryRowContext(ctx, "SELECT COALESCE(services_json, '') FROM crews WHERE id = ?", crew.ID).Scan(&raw); err != nil {
			return fmt.Errorf("backup: cannot read service configuration: %w", err)
		}
		if err := requireSupportedServiceConfig(raw, runtime != nil && runtime.QuotaSnapshotNamespace() != ""); err != nil {
			return err
		}
	}
	return nil
}

// Instance bundles require transport for every live quota-service declaration.
// The captured images are also checked against the immutable database image.
func requireSupportedInstanceServiceBackups(ctx context.Context, db *sql.DB, runtime ServiceSnapshotRuntime) error {
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(c.services_json, '') FROM crews c
		JOIN workspaces w ON w.id=c.workspace_id
		WHERE (c.deleted_at IS NULL OR c.deleted_at='') AND (w.deleted_at IS NULL OR w.deleted_at='')`)
	if err != nil {
		return fmt.Errorf("backup: cannot read service configuration: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("backup: cannot read service configuration: %w", err)
		}
		if err := requireSupportedServiceConfig(raw, runtime != nil && runtime.QuotaSnapshotNamespace() != ""); err != nil {
			return err
		}
	}
	return rows.Err()
}

func requireSupportedDumpServiceBackups(dump *DBDump, captured []serviceSnapshot) error {
	expected := map[string]serviceSnapshot{}
	for _, proof := range captured {
		if _, exists := expected[proof.name()]; exists {
			return errors.New("backup: duplicate captured quota image")
		}
		expected[proof.name()] = proof
	}
	intents := map[string]map[string]any{}
	for _, row := range dump.Tables["service_runtime_intents"] {
		crew, _ := row["crew_id"].(string)
		service, _ := row["service_name"].(string)
		key := crew + "\x00" + service
		if _, exists := intents[key]; exists {
			return errors.New("backup: duplicate quota service intent")
		}
		intents[key] = row
	}
	for _, row := range dump.Tables["crews"] {
		if rowIsDeleted(row) {
			continue
		}
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
		if err := requireSupportedServiceConfig(raw, true); err != nil {
			return err
		}
		crew, _ := row["id"].(string)
		slug, _ := row["slug"].(string)
		declared, err := declaredServiceSnapshots(raw, crew, slug)
		if err != nil {
			return errors.New("backup: invalid quota service declaration")
		}
		for _, spec := range declared {
			proof, exists := expected[spec.name()]
			if !exists || spec.key() != proof.key() || spec.Bytes != proof.Bytes || spec.CrewSlug != proof.CrewSlug || proof.Namespace == "" || !snapshotEntryName.MatchString(proof.SHA256+".json") {
				return errors.New("backup: quota snapshot transport did not capture immutable dump declaration")
			}
			intent := intents[crew+"\x00"+spec.Service]
			version, err := rowInt64(intent, "version")
			desired, _ := intent["desired_state"].(string)
			if err != nil || version != proof.IntentVersion || desired != proof.DesiredState {
				return errors.New("backup: captured quota service intent revision changed")
			}
			delete(expected, spec.name())
		}
	}
	if len(expected) != 0 {
		return errors.New("backup: captured quota image missing from immutable dump")
	}
	return nil
}

func requireSupportedServiceConfig(raw string, transportAvailable bool) error {
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
		if service.QuotaEnforced && len(service.Volumes) != 0 && !transportAvailable {
			return errors.New("backup: standalone quota service data backup unsupported until snapshot transport is configured")
		}
	}
	return nil
}

// Tombstones remain in logical dumps for history, but have no live disk promise.
func rowIsDeleted(row map[string]any) bool {
	switch value := row["deleted_at"].(type) {
	case nil:
		return false
	case string:
		return value != ""
	case []byte:
		return len(value) != 0
	default:
		return true
	}
}
