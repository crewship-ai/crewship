package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// The instance-wide provider allowlist had an API but no CLI, while
// docs/guides/notifications.mdx told operators to use
// `crewship notifychannel providers` to disable one. It lists; it could not
// toggle. These pin the command that closes that gap — and the repo rule it
// broke: every /api/v1 route gets a CLI command in the same PR.

func TestNotifyChannelProvidersToggle_SendsPatch(t *testing.T) {
	covSaveState(t)

	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"discord","enabled":false}`))
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "ws_1", Server: srv.URL}

	if err := notifyChannelProvidersDisableCmd.RunE(notifyChannelProvidersDisableCmd, []string{"discord"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/api/v1/notification-providers/discord" {
		t.Errorf("path = %s, want /api/v1/notification-providers/discord", gotPath)
	}
	// The flag is the whole payload: sending the wrong boolean would enable a
	// provider an admin asked to switch off.
	if v, ok := gotBody["enabled"].(bool); !ok || v {
		t.Errorf("body enabled = %v, want false", gotBody["enabled"])
	}

	gotBody = nil
	if err := notifyChannelProvidersEnableCmd.RunE(notifyChannelProvidersEnableCmd, []string{"discord"}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if v, ok := gotBody["enabled"].(bool); !ok || !v {
		t.Errorf("body enabled = %v, want true", gotBody["enabled"])
	}
}

// A 403 from the roleManage gate must reach the operator as the reason they
// were refused, not as a generic failure.
func TestNotifyChannelProvidersToggle_SurfacesForbidden(t *testing.T) {
	covSaveState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"requires ADMIN"}}`))
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "ws_1", Server: srv.URL}

	err := notifyChannelProvidersDisableCmd.RunE(notifyChannelProvidersDisableCmd, []string{"discord"})
	if err == nil {
		t.Fatal("a 403 must not be reported as success")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "admin") {
		t.Errorf("error %q should carry the server's reason", err)
	}
}

func TestNotifyChannelProvidersToggle_AuthGates(t *testing.T) {
	covSaveState(t)
	for name, c := range map[string]*cobra.Command{
		"enable":  notifyChannelProvidersEnableCmd,
		"disable": notifyChannelProvidersDisableCmd,
	} {
		cliCfg = &cli.CLIConfig{}
		if err := c.RunE(c, []string{"discord"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected not-logged-in; got %v", name, err)
		}
		cliCfg = &cli.CLIConfig{Token: "tok"}
		if err := c.RunE(c, []string{"discord"}); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error; got %v", name, err)
		}
	}
}
