package database

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestMigrateSkippingRefusesEveryUnknownName(t *testing.T) {
	for _, tc := range []struct {
		name         string
		first        string
		dependencies []string
	}{
		{name: "unknown foundation", first: "not_a_migration"},
		{name: "unknown dependency", first: "access_preset_selections", dependencies: []string{"not_a_migration"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := migrationBoundaryDB(t)
			err := MigrateSkipping(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil)), tc.first, tc.dependencies...)
			if err == nil || !strings.Contains(err.Error(), "not_a_migration") {
				t.Fatalf("unknown requested name accepted: %v", err)
			}
			var applied int
			if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&applied); err != nil || applied != 0 {
				t.Fatalf("invalid fixture applied migrations: %d %v", applied, err)
			}
		})
	}
}
