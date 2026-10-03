package notify_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
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

func TestAgentChannelPairingIsScopedIdempotentAndRedactsDestinations(t *testing.T) {
	channels, pairs, _ := notificationStores(t)
	const ws = "notification-ws1"
	const agent = "agent-notification-ws1"
	first := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelWebhook, URL: "https://hooks.example.test/team", Secret: "synthetic-signing-value"})
	second := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test"})
	if paired, err := pairs.IsPaired(t.Context(), ws, first.ID, agent); err != nil || paired {
		t.Fatalf("default grant: %v %v", paired, err)
	}
	if err := pairs.Allow(t.Context(), ws, first.ID, agent, "operator-original"); err != nil {
		t.Fatal(err)
	}
	if paired, err := pairs.IsPaired(t.Context(), ws, first.ID, agent); err != nil || !paired {
		t.Fatalf("committed grant did not authorize its bound agent: %v %v", paired, err)
	}
	if err := pairs.Allow(t.Context(), ws, first.ID, agent, "operator-repeated"); err != nil {
		t.Fatal(err)
	}
	grants, err := pairs.ListForChannel(t.Context(), ws, first.ID)
	if err != nil || len(grants) != 1 || grants[0].GrantedBy != "operator-original" || grants[0].AgentID != agent || grants[0].ID == "" || grants[0].CreatedAt == "" {
		t.Fatalf("idempotent pairing lost provenance: %#v %v", grants, err)
	}
	if err := pairs.Allow(t.Context(), ws, second.ID, agent, ""); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if ok, err := channels.Patch(t.Context(), ws, second.ID, notify.PatchInput{Enabled: &disabled}); err != nil || !ok {
		t.Fatalf("disable: %v %v", ok, err)
	}
	listed, err := pairs.ListForAgent(t.Context(), ws, agent)
	if err != nil || len(listed) != 2 {
		t.Fatalf("agent discovery: %#v %v", listed, err)
	}
	for _, channel := range listed {
		if channel.URL != "" || channel.To != "" || channel.Secret != "" {
			t.Fatal("agent discovery exposed destination or signing material")
		}
		if channel.WorkspaceID != ws {
			t.Fatal("discovery changed workspace")
		}
		if channel.ID == second.ID && channel.Enabled {
			t.Fatal("disabled channel advertised as enabled")
		}
	}
	for _, scope := range []struct{ ws, channel, agent string }{{"notification-ws2", first.ID, agent}, {ws, first.ID, "agent-notification-ws2"}, {"", first.ID, agent}, {ws, "", agent}, {ws, first.ID, ""}} {
		if paired, err := pairs.IsPaired(t.Context(), scope.ws, scope.channel, scope.agent); err != nil || paired {
			t.Fatalf("foreign or incomplete scope authorized: %v %v", paired, err)
		}
	}
	if got, err := pairs.ListForAgent(t.Context(), "notification-ws2", agent); err != nil || len(got) != 0 {
		t.Fatal("agent discovery crossed workspace")
	}
	if got, err := pairs.ListForChannel(t.Context(), "notification-ws2", first.ID); err != nil || len(got) != 0 {
		t.Fatal("pairing audit crossed workspace")
	}
	if removed, err := pairs.Deny(t.Context(), "notification-ws2", first.ID, agent); err != nil || removed {
		t.Fatal("foreign workspace revoked grant")
	}
	if removed, err := pairs.Deny(t.Context(), ws, first.ID, agent); err != nil || !removed {
		t.Fatalf("revoke: %v %v", removed, err)
	}
	if removed, err := pairs.Deny(t.Context(), ws, first.ID, agent); err != nil || removed {
		t.Fatal("repeated revocation invented removal")
	}
	if paired, err := pairs.IsPaired(t.Context(), ws, first.ID, agent); err != nil || paired {
		t.Fatal("revoked grant still authorizes")
	}
	if removed, err := channels.Delete(t.Context(), ws, second.ID); err != nil || !removed {
		t.Fatalf("delete: %v %v", removed, err)
	}
	if got, err := pairs.ListForAgent(t.Context(), ws, agent); err != nil || len(got) != 0 {
		t.Fatal("soft-deleted channel remains discoverable")
	}
}

func TestPairingValidationAndStorageFailuresNeverAuthorize(t *testing.T) {
	channels, pairs, db := notificationStores(t)
	const ws = "notification-ws1"
	const agent = "agent-notification-ws1"
	channel := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test"})
	for _, scope := range []struct{ ws, channel, agent string }{{"", channel.ID, agent}, {ws, "", agent}, {ws, channel.ID, ""}} {
		if err := pairs.Allow(t.Context(), scope.ws, scope.channel, scope.agent, "operator"); err == nil {
			t.Fatal("incomplete grant accepted")
		}
	}
	if err := pairs.Allow(t.Context(), ws, "nonexistent-channel", agent, "operator"); err == nil {
		t.Fatal("foreign-key violation acknowledged")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := pairs.Allow(ctx, ws, channel.ID, agent, "operator"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled grant: %v", err)
	}
	if err := pairs.Allow(t.Context(), ws, channel.ID, agent, "operator"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pairs.Allow(t.Context(), ws, channel.ID, agent, "operator"); err == nil {
		t.Fatal("closed ledger acknowledged grant")
	}
	if ok, err := pairs.Deny(t.Context(), ws, channel.ID, agent); err == nil || ok {
		t.Fatal("closed ledger acknowledged revocation")
	}
	if ok, err := pairs.IsPaired(t.Context(), ws, channel.ID, agent); err == nil || ok {
		t.Fatal("unreadable ledger authorized delivery")
	}
	if list, err := pairs.ListForAgent(t.Context(), ws, agent); err == nil || list != nil {
		t.Fatal("unreadable ledger invented discovery")
	}
	if list, err := pairs.ListForChannel(t.Context(), ws, channel.ID); err == nil || list != nil {
		t.Fatal("unreadable ledger invented grants")
	}
}

func TestChannelPatchKeepsWorkspaceBoundariesAndUnspecifiedFields(t *testing.T) {
	channels, _, _ := notificationStores(t)
	const ws = "notification-ws1"
	channel := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelWebhook, URL: "https://hooks.example.test/team", Secret: "synthetic-signing-value"})
	disabled, enabled := false, true
	categories := []string{" CHAT.REPLIES ", "chat.replies"}
	events := []string{"success", "run.completed", "failure"}
	priority := " HIGH "
	patch := notify.PatchInput{Enabled: &disabled, Categories: &categories, Events: &events, MinPriority: &priority}
	if changed, err := channels.Patch(t.Context(), "notification-ws2", channel.ID, patch); err != nil || changed {
		t.Fatal("foreign workspace patched channel")
	}
	if changed, err := channels.Patch(t.Context(), ws, channel.ID, patch); err != nil || !changed {
		t.Fatalf("patch: %v %v", changed, err)
	}
	got, err := channels.GetForDispatch(t.Context(), ws, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.MinPriority != "high" || !reflect.DeepEqual(got.Categories, []string{"chat.replies"}) || !got.Wants(notify.EventRunCompleted) || !got.Wants(notify.EventRunFailed) {
		t.Fatalf("patch did not persist filters: %#v", got)
	}
	if got.URL != channel.URL || got.Secret != channel.Secret {
		t.Fatal("filter patch changed delivery credentials")
	}
	if got.AllowsCategory("security.alerts") || !got.AllowsCategory("chat.replies") {
		t.Fatal("category allowlist ignored")
	}
	empty := []string{}
	if changed, err := channels.Patch(t.Context(), ws, channel.ID, notify.PatchInput{Enabled: &enabled, Categories: &empty}); err != nil || !changed {
		t.Fatalf("clear filter: %v %v", changed, err)
	}
	got, err = channels.GetForDispatch(t.Context(), ws, channel.ID)
	if err != nil || !got.Enabled || !got.AllowsCategory("security.alerts") || got.MinPriority != "high" || !reflect.DeepEqual(got.Events, []string{notify.EventRunCompleted, notify.EventRunFailed}) {
		t.Fatalf("partial patch lost unrelated settings: %#v %v", got, err)
	}
	if _, err := channels.Delete(t.Context(), ws, channel.ID); err != nil {
		t.Fatal(err)
	}
	if changed, err := channels.Patch(t.Context(), ws, channel.ID, patch); err != nil || changed {
		t.Fatal("patch resurrected soft-deleted channel")
	}
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

func TestChannelPatchRejectsInvalidInputWithoutPartialChanges(t *testing.T) {
	channels, _, _ := notificationStores(t)
	const ws = "notification-ws1"
	channel := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test"})
	before, err := channels.GetForDispatch(t.Context(), ws, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	invalidCategories := []string{"unknown-category"}
	invalidEvents := []string{"unknown-event"}
	invalidPriority := "critical"
	for _, patch := range []notify.PatchInput{{}, {Enabled: &disabled, Categories: &invalidCategories}, {Enabled: &disabled, Events: &invalidEvents}, {Enabled: &disabled, MinPriority: &invalidPriority}} {
		if ok, err := channels.Patch(t.Context(), ws, channel.ID, patch); err == nil || ok {
			t.Fatalf("invalid patch accepted: %v %v", ok, err)
		}
		after, err := channels.GetForDispatch(t.Context(), ws, channel.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("invalid patch partially altered channel")
		}
	}
}

func TestChannelVisibilitySeparatesPersonalChannelsFromWorkspaceFanout(t *testing.T) {
	channels, _, _ := notificationStores(t)
	const ws = "notification-ws1"
	shared := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelWebhook, URL: "https://hooks.example.test/shared"})
	own := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelWebhook, URL: "https://hooks.example.test/private-one", Scope: notify.ScopeUser, OwnerUserID: "notification-user1"})
	other := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelWebhook, URL: "https://hooks.example.test/private-two", Scope: notify.ScopeUser, OwnerUserID: "notification-user2"})
	disabled := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "disabled@example.test"})
	off := false
	if _, err := channels.Patch(t.Context(), ws, disabled.ID, notify.PatchInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	all, err := channels.ListAll(t.Context(), ws)
	if err != nil || len(all) != 4 {
		t.Fatalf("admin inventory: %d %v", len(all), err)
	}
	for _, c := range all {
		if c.Secret != "" {
			t.Fatal("admin inventory exposed plaintext signing value")
		}
	}
	visible, err := channels.List(t.Context(), ws, "notification-user1")
	if err != nil || len(visible) != 3 {
		t.Fatalf("personal inventory: %d %v", len(visible), err)
	}
	for _, c := range visible {
		if c.ID == other.ID || c.Secret != "" {
			t.Fatal("personal listing exposed another owner's channel or secret")
		}
	}
	deliveries, err := channels.ListForUser(t.Context(), ws, "notification-user1")
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("delivery inventory: %d %v", len(deliveries), err)
	}
	for _, c := range deliveries {
		if c.ID != own.ID && c.ID != shared.ID {
			t.Fatal("delivery included disabled or another person's channel")
		}
		if c.Secret == "" {
			t.Fatal("delivery signing value absent")
		}
	}
	fanout, err := channels.ListEnabled(t.Context(), ws)
	if err != nil || len(fanout) != 1 || fanout[0].ID != shared.ID {
		t.Fatalf("legacy fanout included personal channel: %d %v", len(fanout), err)
	}
}

func TestClosedChannelStoreDoesNotAcknowledgeWritesOrReturnDestinations(t *testing.T) {
	channels, _, db := notificationStores(t)
	const ws = "notification-ws1"
	channel := createNotificationChannel(t, channels, notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test"})
	public, err := channels.Get(t.Context(), ws, channel.ID)
	if err != nil || public.ID != channel.ID || public.Secret != "" {
		t.Fatal("public read did not return a redacted channel")
	}
	if _, err := channels.Get(t.Context(), "notification-ws2", channel.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("public read crossed workspace: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := channels.Get(t.Context(), ws, channel.ID); err == nil {
		t.Fatal("closed public read returned a channel")
	}
	if ch, err := channels.Create(t.Context(), notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test"}); err == nil || ch.ID != "" {
		t.Fatal("closed store acknowledged creation")
	}
	enabled := true
	if ok, err := channels.Patch(t.Context(), ws, channel.ID, notify.PatchInput{Enabled: &enabled}); err == nil || ok {
		t.Fatal("closed store acknowledged patch")
	}
	if ok, err := channels.Delete(t.Context(), ws, channel.ID); err == nil || ok {
		t.Fatal("closed store acknowledged delete")
	}
	if ch, err := channels.GetForDispatch(t.Context(), ws, channel.ID); err == nil || ch.ID != "" {
		t.Fatal("closed store invented dispatch target")
	}
	for name, read := range map[string]func() ([]notify.Channel, error){
		"list":          func() ([]notify.Channel, error) { return channels.List(t.Context(), ws, "notification-user1") },
		"list all":      func() ([]notify.Channel, error) { return channels.ListAll(t.Context(), ws) },
		"list enabled":  func() ([]notify.Channel, error) { return channels.ListEnabled(t.Context(), ws) },
		"list for user": func() ([]notify.Channel, error) { return channels.ListForUser(t.Context(), ws, "notification-user1") },
	} {
		got, err := read()
		if err == nil || got != nil {
			t.Fatalf("%s invented destinations after read failure", name)
		}
	}
}

func TestChannelCreationRejectsInvalidScopeFiltersAndDestinations(t *testing.T) {
	channels, _, db := notificationStores(t)
	const ws = "notification-ws1"
	for _, change := range []func(*notify.ChannelInput){
		func(in *notify.ChannelInput) { in.Scope = "unknown" },
		func(in *notify.ChannelInput) { in.Scope = notify.ScopeUser },
		func(in *notify.ChannelInput) { in.OwnerUserID = "notification-user1" },
		func(in *notify.ChannelInput) { in.Categories = []string{"unknown"} },
		func(in *notify.ChannelInput) { in.MinPriority = "critical" },
		func(in *notify.ChannelInput) { in.Type = "unknown" },
		func(in *notify.ChannelInput) { in.Type = notify.ChannelWebhook; in.URL = " " },
		func(in *notify.ChannelInput) { in.Type = notify.ChannelWebhook; in.URL = "https://[broken" },
		func(in *notify.ChannelInput) { in.Type = notify.ChannelWebhook; in.URL = "https:///missing-host" },
		func(in *notify.ChannelInput) {
			in.Type = notify.ChannelShoutrrr
			in.Provider = "slack"
			in.Fields = map[string]string{"webhook_url": "bad-url"}
		},
	} {
		in := notify.ChannelInput{WorkspaceID: ws, Type: notify.ChannelEmail, To: "private@example.test"}
		change(&in)
		if channel, err := channels.Create(t.Context(), in); err == nil || channel.ID != "" {
			t.Fatalf("invalid channel acknowledged: %v", err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM notification_channels WHERE workspace_id=?`, ws).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid channel persisted: %d %v", count, err)
	}
}

func TestChannelEncryptionFailureDoesNotPersistPlaintext(t *testing.T) {
	channels, _, db := notificationStores(t)
	t.Setenv("ENCRYPTION_KEY", "invalid-test-key")
	for _, in := range []notify.ChannelInput{
		{WorkspaceID: "notification-ws1", Type: notify.ChannelWebhook, URL: "https://hooks.example.test/team", Secret: "synthetic-signing-value"},
		{WorkspaceID: "notification-ws1", Type: notify.ChannelShoutrrr, Provider: "slack", ShoutrrrURL: "slack://synthetic@channel"},
	} {
		if channel, err := channels.Create(t.Context(), in); err == nil || channel.ID != "" {
			t.Fatal("unencryptable delivery secret acknowledged")
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM notification_channels WHERE workspace_id='notification-ws1'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed encryption left a channel")
	}
}

func TestTemplateStorageFailuresCannotInventSuccessfulChanges(t *testing.T) {
	_, _, db := notificationStores(t)
	store := notify.NewTemplateStore(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.List(t.Context(), "notification-ws1"); err == nil || rows != nil {
		t.Fatal("closed template catalog returned records")
	}
	if err := store.Upsert(t.Context(), "notification-ws1", notify.MessageTemplate{Category: "chat.replies", Title: "Custom title"}); err == nil {
		t.Fatal("closed template catalog acknowledged save")
	}
	if err := store.Delete(t.Context(), "notification-ws1", "chat.replies", ""); err == nil {
		t.Fatal("closed template catalog acknowledged delete")
	}
	if template, err := store.Resolve(t.Context(), "notification-ws1", "chat.replies", "channel"); err == nil || template != (notify.MessageTemplate{}) {
		t.Fatal("closed template catalog invented wording")
	}
	if enabled, err := notify.DefaultProviderGate(db)(t.Context(), "slack"); err == nil || enabled {
		t.Fatal("unreadable instance settings enabled provider")
	}
}

func TestNotificationPriorityAndTerminalStatusContracts(t *testing.T) {
	for input, want := range map[string]int{"urgent": 3, "high": 2, "medium": 1, "low": 0, "": 0, "misspelled": 0} {
		if got := notify.PriorityRank(input); got != want {
			t.Errorf("priority %q ranks %d, want %d", input, got, want)
		}
	}
	for input, want := range map[string]string{"completed": notify.EventRunCompleted, "failed": notify.EventRunFailed, "running": "", "cancelled": "", "unknown": ""} {
		if got := notify.EventTypeForStatus(input); got != want {
			t.Errorf("status %q emitted %q, want %q", input, got, want)
		}
	}
	if group := notify.GroupForCategory("unknown"); group != "" {
		t.Fatalf("unknown category inherited group %q", group)
	}
}

func TestNotificationTemplatesPreserveSourceAndTolerateMissingFacts(t *testing.T) {
	message := notify.CategoryMessage{Title: "Original title", Body: "Original body", SourceKind: "message", Category: "chat.replies", Vars: map[string]any{"scalar": "plain", "nested": map[string]any{"present": "value"}, "nil": nil}}
	if got := notify.RenderTemplate("{{source.body}} / {{source.kind}}", message); got != "Original body / message" {
		t.Fatalf("source facts: %q", got)
	}
	for _, token := range []string{"vars.scalar.child", "vars.nested.absent", "vars.nil"} {
		if got := notify.RenderTemplate("before {{"+token+"}} after", message); got != "before  after" {
			t.Fatalf("missing fact %q: %q", token, got)
		}
	}
	for _, token := range []string{"source.title", "source.body", "source.kind", "source.category"} {
		if err := notify.ValidateTemplate(notify.MessageTemplate{Category: "chat.replies", Title: "{{" + token + "}}"}); err != nil {
			t.Fatalf("valid source reference rejected: %v", err)
		}
	}
	for _, token := range []string{"source", "source."} {
		if err := notify.ValidateTemplate(notify.MessageTemplate{Category: "chat.replies", Body: "{{" + token + "}}"}); err == nil {
			t.Fatalf("incomplete reference %q accepted", token)
		}
	}
}

func TestNotificationProviderBindingRejectsUnusableOrDifferentService(t *testing.T) {
	for _, input := range []struct{ url, provider string }{{"slack://synthetic@channel", "unknown"}, {"https://[broken", "slack"}, {"slack:///", "slack"}, {"generic://example.test", "slack"}} {
		if err := notify.ValidateServiceURLForProvider(input.url, input.provider); err == nil {
			t.Fatal("unusable or differently bound provider URL accepted")
		}
	}
	if err := notify.ValidateServiceURLForProvider("slack://synthetic@channel", "slack"); err != nil {
		t.Fatalf("valid provider binding refused: %v", err)
	}
	for _, raw := range []string{"", "unknown-scheme://example.test"} {
		if err := notify.ValidateServiceURL(raw); err == nil {
			t.Fatal("unsupported service accepted")
		}
	}
	spec, ok := notify.ProviderByName("slack")
	if !ok {
		t.Fatal("slack provider missing")
	}
	if field := spec.FieldByKey("webhook_url"); field == nil || !field.Required {
		t.Fatal("provider form lost required webhook field")
	}
	if field := spec.FieldByKey("not-a-field"); field != nil {
		t.Fatal("unknown provider field invented")
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
