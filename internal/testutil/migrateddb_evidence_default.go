//go:build !linux || !mooring_rust_integration

package testutil

import (
	"database/sql"
	"testing"
)

func actualEvidenceEnabled() bool                 { return false }
func newActualEvidenceDBDir() (string, error)     { panic("integration evidence unavailable") }
func snapshotActualFailure(testing.TB, string)    {}
func retainActualFailure(testing.TB, string) bool { return false }

func SnapshotActualFailure(testing.TB, *sql.DB, string) {}

func secureActualDB(string) error { return nil }

func ActualEvidenceDBPath(_ testing.TB, original string) string { return original }
