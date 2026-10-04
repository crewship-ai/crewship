package governance

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestStorageFailureDoesNotInventGovernanceSettings(t *testing.T) {
	db := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, found, err := Get(ctx, db, "ws1"); err == nil || found {
		t.Fatalf("closed Get found=%v error=%v", found, err)
	}
	if _, found, err := Defaults(ctx, db); err == nil || found {
		t.Fatalf("closed Defaults found=%v error=%v", found, err)
	}
	for name, err := range map[string]error{
		"upsert":   Upsert(ctx, db, "ws1", Settings{Enabled: true}, ""),
		"defaults": SetDefaults(ctx, db, Settings{Enabled: true}),
		"seed":     SeedWorkspace(ctx, db, "ws1"),
	} {
		if err == nil {
			t.Errorf("%s swallowed storage failure", name)
		}
	}
	var log bytes.Buffer
	got := Resolve(ctx, db, slog.New(slog.NewTextHandler(&log, nil)), "ws1")
	if got.Enabled || got.DenyNotifyMinRisk != DefaultDenyNotifyMinRisk {
		t.Fatalf("resolve invented settings: %+v", got)
	}
	if !strings.Contains(log.String(), "resolve failed") {
		t.Fatal("failure was not logged")
	}
	if got := Resolve(ctx, db, nil, "ws1"); got.Enabled {
		t.Fatal("nil logger changed fallback")
	}
}

func TestMalformedDefaultsCannotSeedAWorkspace(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO app_settings(key,value) VALUES (?,?)`, DefaultsSettingKey, `{"enabled":`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := Defaults(ctx, db); err == nil || found {
		t.Fatalf("corrupt defaults found=%v err=%v", found, err)
	}
	if err := SeedWorkspace(ctx, db, "ws1"); err == nil {
		t.Fatal("corrupt defaults were applied")
	}
	if _, found, err := Get(ctx, db, "ws1"); err != nil || found {
		t.Fatalf("failed seed left a row: found=%v err=%v", found, err)
	}
}
