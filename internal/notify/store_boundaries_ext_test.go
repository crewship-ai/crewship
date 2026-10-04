package notify_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func notificationStores(t *testing.T) (*notify.ChannelStore, *notify.PairingStore, *sql.DB) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("01", 32))
	db := testutil.MigratedSQLDB(t)
	for _, ws := range []string{"notification-ws1", "notification-ws2"} {
		if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,?,?)`, ws, ws, ws); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name,slug) VALUES(?,?,?,?)`, "agent-"+ws, ws, "Agent", "agent"); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{"notification-user1", "notification-user2"} {
		if _, err := db.Exec(`INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	return notify.NewChannelStore(db), notify.NewPairingStore(db), db
}

func createNotificationChannel(t *testing.T, channels *notify.ChannelStore, in notify.ChannelInput) notify.Channel {
	t.Helper()
	channel, err := channels.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func TestCorruptChannelFiltersCannotBroadenDeliveryAuthority(t *testing.T) {
	channels, _, db := notificationStores(t)
	const ws = "notification-ws1"
	channel := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test", Categories: []string{"chat.replies"}})
	for _, column := range []string{"categories_json", "events_json", "config_json"} {
		t.Run(column, func(t *testing.T) {
			var original string
			if err := db.QueryRow("SELECT "+column+" FROM notification_channels WHERE id=?", channel.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE notification_channels SET "+column+"=? WHERE id=?", "{unreadable", channel.ID); err != nil {
				t.Fatal(err)
			}
			for name, read := range map[string]func() ([]notify.Channel, error){
				"delivery":        func() ([]notify.Channel, error) { return channels.ListForUser(t.Context(), ws, "notification-user1") },
				"legacy delivery": func() ([]notify.Channel, error) { return channels.ListEnabled(t.Context(), ws) },
				"admin":           func() ([]notify.Channel, error) { return channels.ListAll(t.Context(), ws) },
			} {
				list, err := read()
				if err == nil || len(list) != 0 {
					t.Errorf("%s silently accepted unreadable %s and could broaden delivery authority", name, column)
				}
			}
			if _, err := db.Exec("UPDATE notification_channels SET "+column+"=? WHERE id=?", original, channel.ID); err != nil {
				t.Fatal(err)
			}
			restored, err := channels.GetForDispatch(t.Context(), ws, channel.ID)
			if err != nil || !restored.AllowsCategory("chat.replies") || restored.AllowsCategory("system.health") {
				t.Fatalf("restored filter: %#v %v", restored, err)
			}
		})
	}
}

func TestLegacyEmptyChannelFiltersRemainReadable(t *testing.T) {
	channels, _, db := notificationStores(t)
	const ws = "notification-ws1"
	channel := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test"})
	for _, categories := range []string{"", "null", "[]"} {
		if _, err := db.Exec(`UPDATE notification_channels SET categories_json=?, events_json='' WHERE id=?`, categories, channel.ID); err != nil {
			t.Fatal(err)
		}
		got, err := channels.GetForDispatch(t.Context(), ws, channel.ID)
		if err != nil {
			t.Fatalf("legacy filter %q refused: %v", categories, err)
		}
		if !got.AllowsCategory("chat.replies") || !got.AllowsCategory("system.health") || !got.Wants(notify.EventRunFailed) || got.Wants(notify.EventRunCompleted) {
			t.Fatal("legacy default filtering changed")
		}
	}
}
