package notify

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// The instance-wide provider toggle was enforced at channel-CREATE time and
// nowhere else. An operator who switched Discord off to stop a leak kept
// receiving Discord messages: every channel created before the switch went on
// delivering, because nothing consulted the toggle on the way out.
//
// A switch labelled "enable/disable" has to mean "nothing leaves through this
// provider", or it is a switch that lies at exactly the moment someone reaches
// for it.

type gateProvider struct{ sent []string }

func (p *gateProvider) Send(_ context.Context, rawURL, message string, _ map[string]string) error {
	p.sent = append(p.sent, rawURL+"|"+message)
	return nil
}

func gateChannel() Channel {
	return Channel{
		ID: "ch-1", WorkspaceID: "ws-1", Type: ChannelShoutrrr,
		Provider: "discord", Secret: "discord://token@id", Enabled: true,
	}
}

func TestDispatcher_DisabledProviderStopsAnExistingChannel(t *testing.T) {
	prov := &gateProvider{}
	defer SetProviderForTesting(prov)()
	d := NewDispatcher(nil, nil, nil, nil)
	d.SetProviderGate(func(_ context.Context, name string) (bool, error) {
		return name != "discord", nil
	})

	err := d.deliverShoutrrr(context.Background(), gateChannel(), NotificationEvent{Type: "run.failed", WorkspaceID: "ws-1"})
	if err == nil {
		t.Fatal("delivery through a disabled provider was allowed")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error does not say why it refused: %v", err)
	}
	if len(prov.sent) != 0 {
		t.Fatalf("the message went out anyway: %v", prov.sent)
	}
}

func TestDispatcher_DisabledProviderStopsCategoryMessagesToo(t *testing.T) {
	prov := &gateProvider{}
	defer SetProviderForTesting(prov)()
	d := NewDispatcher(nil, nil, nil, nil)
	d.SetProviderGate(func(context.Context, string) (bool, error) { return false, nil })

	err := d.deliverCategoryShoutrrr(context.Background(), gateChannel(),
		CategoryMessage{Category: "run.failed", Title: "t", Body: "b"})
	if err == nil || len(prov.sent) != 0 {
		t.Fatalf("category message escaped a disabled provider (err=%v, sent=%v)", err, prov.sent)
	}
}

func TestDispatcher_EnabledProviderStillDelivers(t *testing.T) {
	prov := &gateProvider{}
	defer SetProviderForTesting(prov)()
	d := NewDispatcher(nil, nil, nil, nil)
	d.SetProviderGate(func(context.Context, string) (bool, error) { return true, nil })

	if err := d.deliverShoutrrr(context.Background(), gateChannel(), NotificationEvent{Type: "run.failed", WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("enabled provider refused: %v", err)
	}
	if len(prov.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(prov.sent))
	}
}

// A gate that cannot answer must not become a silent "yes". The toggle exists
// to stop data leaving; when its state is unknown, holding the message is the
// safe direction and the operator gets an error to act on.
func TestDispatcher_UnreadableGateFailsClosed(t *testing.T) {
	prov := &gateProvider{}
	defer SetProviderForTesting(prov)()
	d := NewDispatcher(nil, nil, nil, nil)
	d.SetProviderGate(func(context.Context, string) (bool, error) {
		return false, errors.New("database is locked")
	})

	if err := d.deliverShoutrrr(context.Background(), gateChannel(), NotificationEvent{Type: "run.failed", WorkspaceID: "ws-1"}); err == nil {
		t.Fatal("an unreadable gate allowed the send")
	}
	if len(prov.sent) != 0 {
		t.Fatalf("message sent despite an unreadable gate: %v", prov.sent)
	}
}

// No gate wired (embedded use, tests) keeps the previous behaviour rather
// than blocking every notification on this instance.
func TestDispatcher_NoGateDeliversAsBefore(t *testing.T) {
	prov := &gateProvider{}
	defer SetProviderForTesting(prov)()
	d := NewDispatcher(nil, nil, nil, nil)

	if err := d.deliverShoutrrr(context.Background(), gateChannel(), NotificationEvent{Type: "run.failed", WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("refused with no gate configured: %v", err)
	}
	if len(prov.sent) != 1 {
		t.Fatalf("sent %d, want 1", len(prov.sent))
	}
}

// The gate used to be opt-in: NewDispatcher left providerGate nil and each
// call site was expected to remember SetProviderGate. Two of the four
// production sites in cmd_start.go did not, so a provider switched off
// stopped the channel-created path and kept delivering on the router and
// scheduled-run paths. Whether the kill switch worked depended on which code
// sent the message. Installing the gate in the constructor is what makes that
// unreachable rather than merely unlikely.
func TestNewDispatcher_InstallsTheProviderGateByDefault(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE app_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO app_settings (key, value) VALUES (?, 'false')`,
		ProviderSettingKey("discord")); err != nil {
		t.Fatal(err)
	}

	prov := &gateProvider{}
	defer SetProviderForTesting(prov)()

	// No SetProviderGate call anywhere — this is the whole point.
	d := NewDispatcher(nil, nil, nil, db)

	err = d.deliverShoutrrr(context.Background(), gateChannel(),
		NotificationEvent{Type: "run.failed", WorkspaceID: "ws-1"})
	if err == nil {
		t.Fatal("a dispatcher built the ordinary way delivered through a disabled provider")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error does not say why it refused: %v", err)
	}
	if len(prov.sent) != 0 {
		t.Fatalf("the message went out anyway: %v", prov.sent)
	}

	// A provider with no row is enabled — upgrades must not go silent.
	if err := d.deliverShoutrrr(context.Background(),
		Channel{ID: "ch-2", WorkspaceID: "ws-1", Type: ChannelShoutrrr,
			Provider: "slack", Secret: "slack://token@id", Enabled: true},
		NotificationEvent{Type: "run.failed", WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("a provider with no toggle row was refused: %v", err)
	}
}
