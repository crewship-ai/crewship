package backup

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type ServiceMaintenance struct {
	CrewID       string `json:"crew_id" yaml:"crew_id"`
	CrewSlug     string `json:"crew_slug" yaml:"crew_slug"`
	Operation    string `json:"operation" yaml:"operation"`
	CreatedAt    string `json:"created_at" yaml:"created_at"`
	ProducerLive bool   `json:"producer_live" yaml:"producer_live"`
}

// ServiceMaintenanceStatus exposes selected workspace diagnostics, never the
// opaque epoch, helper namespace, backing image path, or encryption material.
func ServiceMaintenanceStatus(ctx context.Context, db *sql.DB, workspace string) ([]ServiceMaintenance, error) {
	rows, err := db.QueryContext(ctx, `SELECT f.crew_id,c.slug,f.operation,f.created_at,f.producer_until>? FROM service_backup_fences f JOIN crews c ON c.id=f.crew_id WHERE f.workspace_id=? AND c.workspace_id=? ORDER BY c.slug,f.crew_id`, tsformat.Format(time.Now()), workspace, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceMaintenance{}
	for rows.Next() {
		var state ServiceMaintenance
		if err = rows.Scan(&state.CrewID, &state.CrewSlug, &state.Operation, &state.CreatedAt, &state.ProducerLive); err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	return out, rows.Err()
}
