//go:build !linux

package api

import (
	"database/sql"
	"testing"
)

// The caller returns before the Linux provider-authority scale fixture.
func checkNavigationScale(t *testing.T, db *sql.DB, user string, positive bool) {
	t.Fatal("Linux-only scale fixture called on unsupported platform")
}

func checkNavigationProfiles(t *testing.T, db *sql.DB, router *Router, agent string, surfaces func() []any) {
	t.Fatal("Linux-only profile fixture called on unsupported platform")
}
