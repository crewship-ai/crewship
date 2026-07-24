package database

import "testing"

// TestMigrate_V153_NotificationChannelsWidened asserts the v133
// notification_channels table gained the new metadata columns and that
// the type CHECK now admits 'shoutrrr' alongside the original
// 'email'/'webhook' (#1412).
func TestMigrate_V153_NotificationChannelsWidened(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	rows, err := db.Query(`PRAGMA table_info(notification_channels)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	want := map[string]bool{
		"provider": false, "scope": false, "owner_user_id": false,
		"categories_json": false, "min_priority": false,
	}
	for rows.Next() {
		var (
			cid   int
			name  string
			ctype string
			nn    int
			d     *string
			pk    int
		)
		if err := rows.Scan(&cid, &name, &ctype, &nn, &d, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for col, found := range want {
		if !found {
			t.Errorf("notification_channels missing column %q after v153", col)
		}
	}

	// Insert a workspace + user fixture the FK-less insert below can
	// reference loosely (notification_channels has no FK on workspace_id).
	if _, err := db.Exec(`INSERT INTO notification_channels
		(id, workspace_id, type, provider, config_json, events_json, categories_json)
		VALUES ('nch_shoutrrr', 'ws_1', 'shoutrrr', 'slack', '{}', '[]', '[]')`); err != nil {
		t.Fatalf("insert shoutrrr-type channel should succeed post-v153: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO notification_channels
		(id, workspace_id, type, config_json) VALUES ('nch_bad', 'ws_1', 'carrier-pigeon', '{}')`); err == nil {
		t.Fatal("insert with unknown type should still fail the CHECK constraint")
	}
	if _, err := db.Exec(`INSERT INTO notification_channels
		(id, workspace_id, type, config_json, scope) VALUES ('nch_bad_scope', 'ws_1', 'email', '{}', 'nowhere')`); err == nil {
		t.Fatal("insert with unknown scope should fail the CHECK constraint")
	}
}

// TestMigrate_V153_UserNotificationPrefs asserts the matrix table's
// shape: category CHECK (including the '*' mute-all sentinel), state
// CHECK (with the not-yet-used 'digest' value already legal), and the
// (user, category, channel) uniqueness that makes an upsert idempotent.
func TestMigrate_V153_UserNotificationPrefs(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	if _, err := db.Exec(`INSERT INTO notification_channels
		(id, workspace_id, type, config_json) VALUES ('nch_1', 'ws_1', 'webhook', '{}')`); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	for _, category := range []string{
		"approvals", "escalations", "runs.failed", "runs.completed",
		"chat.replies", "security", "budget", "system", "memory", "*",
	} {
		if _, err := db.Exec(`INSERT INTO user_notification_prefs
			(id, workspace_id, user_id, category, channel_id, state)
			VALUES (?, 'ws_1', 'u_1', ?, 'nch_1', 'immediate')`,
			"pref_"+category, category); err != nil {
			t.Errorf("insert pref category=%q should be legal: %v", category, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO user_notification_prefs
		(id, workspace_id, user_id, category, channel_id, state)
		VALUES ('pref_bad', 'ws_1', 'u_1', 'not-a-category', 'nch_1', 'immediate')`); err == nil {
		t.Fatal("insert with unknown category should fail the CHECK constraint")
	}
	if _, err := db.Exec(`INSERT INTO user_notification_prefs
		(id, workspace_id, user_id, category, channel_id, state)
		VALUES ('pref_bad_state', 'ws_1', 'u_1', 'security', 'nch_1', 'weekly')`); err == nil {
		t.Fatal("insert with unknown state should fail the CHECK constraint")
	}
	// 'digest' is legal even though MVP never writes it (schema-ready for v2).
	if _, err := db.Exec(`INSERT INTO user_notification_prefs
		(id, workspace_id, user_id, category, channel_id, state)
		VALUES ('pref_digest', 'ws_1', 'u_2', 'system', 'nch_1', 'digest')`); err != nil {
		t.Errorf("insert with state='digest' should be legal (v2-ready enum): %v", err)
	}
	// UNIQUE(user_id, category, channel_id) makes a re-set an upsert target.
	if _, err := db.Exec(`INSERT INTO user_notification_prefs
		(id, workspace_id, user_id, category, channel_id, state)
		VALUES ('pref_dup', 'ws_1', 'u_1', 'security', 'nch_1', 'off')`); err == nil {
		t.Fatal("duplicate (user, category, channel) should violate the UNIQUE index")
	}
}

// TestMigrate_V153_NotificationDeliveries asserts the outbox/delivery-log
// table's status CHECK and the (channel_id, dedup_key) UNIQUE index that
// backs coalescing — a re-fired source event INSERT-OR-IGNOREs into the
// same row instead of double-delivering.
func TestMigrate_V153_NotificationDeliveries(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	if _, err := db.Exec(`INSERT INTO notification_channels
		(id, workspace_id, type, config_json) VALUES ('nch_1', 'ws_1', 'webhook', '{}')`); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	for _, status := range []string{"pending", "sent", "failed", "dropped_pref", "dropped_rate"} {
		id := "del_" + status
		if _, err := db.Exec(`INSERT INTO notification_deliveries
			(id, workspace_id, channel_id, user_id, category, dedup_key, status)
			VALUES (?, 'ws_1', 'nch_1', 'u_1', 'security', ?, ?)`,
			id, "dedup_"+status, status); err != nil {
			t.Errorf("insert delivery status=%q should be legal: %v", status, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO notification_deliveries
		(id, workspace_id, channel_id, category, dedup_key, status)
		VALUES ('del_bad', 'ws_1', 'nch_1', 'security', 'dedup_bad', 'in-flight')`); err == nil {
		t.Fatal("insert with unknown status should fail the CHECK constraint")
	}

	res, err := db.Exec(`INSERT OR IGNORE INTO notification_deliveries
		(id, workspace_id, channel_id, category, dedup_key, status)
		VALUES ('del_coalesced', 'ws_1', 'nch_1', 'security', 'dedup_pending', 'pending')`)
	if err != nil {
		t.Fatalf("coalescing insert: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 0 {
		t.Fatalf("re-firing the same (channel_id, dedup_key) should be a no-op (coalesced), affected %d rows", n)
	}
}

// TestMigrate_V153_IdempotentReapply guards the writable_schema CHECK
// rewrite specifically: running the full chain twice (the existing
// TestMigrate_Chain_RunTwice_IsIdempotent covers every migration, but this
// pins the v153-specific "already widened" short-circuit) must not error
// and must not double-append 'shoutrrr' into the CHECK text.
func TestMigrate_V153_IdempotentReapply(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	var createSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='notification_channels'`).
		Scan(&createSQL); err != nil {
		t.Fatalf("read notification_channels schema: %v", err)
	}
	if got := countOccurrences(createSQL, "shoutrrr"); got != 1 {
		t.Fatalf("expected exactly one 'shoutrrr' literal in the rewritten CHECK, found %d in: %s", got, createSQL)
	}
}

func countOccurrences(haystack, needle string) int {
	n := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			n++
			i += len(needle) - 1
		}
	}
	return n
}
