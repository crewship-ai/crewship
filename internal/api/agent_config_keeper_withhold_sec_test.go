package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// #1364 R2: the resolver is the single chokepoint. When Keeper is ON, a
// SECRET's decrypted plaintext must NEVER be serialized into the resolved agent
// config — the agent obtains it only via /keeper/request. The orchestrator's
// env/file/MCP gates are defense-in-depth on top of this; if the plaintext never
// leaves the API process, none of those paths can materialize it. Today the
// withholding happens as a side effect of buildKeeperBlock and is untested at
// the response level, so a refactor of the prompt builder could silently
// re-open the leak. These tests pin the invariant on the JSON response itself.

// secretPlaintext is the value seeded below; it must appear in the resolved
// response body only when Keeper is OFF.
const withholdSecretPlaintext = "super-secret-1364"

func seedAgentSecret(t *testing.T, h *InternalHandler, wsID, agentID, name, envVar, credType string) {
	t.Helper()
	enc, err := encryption.Encrypt(withholdSecretPlaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-wh-1364', ?, ?, ?, ?, 'CUSTOM', 'ACTIVE', 'test-user-id')`,
		wsID, name, enc, credType); err != nil {
		t.Fatalf("seed cred: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('ac-wh-1364', ?, 'cr-wh-1364', ?, 1)`, agentID, envVar); err != nil {
		t.Fatalf("seed agent_credentials: %v", err)
	}
}

// credValueFor returns the resolved "value" for the credential with the given
// env_var from the resolve response, and whether such an entry exists.
func credValueFor(t *testing.T, body []byte, envVar string) (string, bool) {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw, _ := resp["credentials"].([]any)
	for _, c := range raw {
		m, _ := c.(map[string]any)
		if m == nil {
			continue
		}
		if ev, _ := m["env_var"].(string); ev == envVar {
			v, _ := m["value"].(string)
			return v, true
		}
	}
	return "", false
}

func TestSecKeeper_SecretValueWithheldFromResponseWhenKeeperOn(t *testing.T) {
	h, wsID, _, agentID := covCfg2Rig(t)
	h.SetKeeperEnabled(true)
	seedAgentSecret(t, h, wsID, agentID, "ProdKey", "PROD_KEY", "SECRET")

	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	// The strongest, tag-agnostic invariant: the plaintext must not appear
	// anywhere in the resolved config the orchestrator receives.
	if strings.Contains(rr.Body.String(), withholdSecretPlaintext) {
		t.Errorf("SECRET plaintext must not be serialized to the resolver response under Keeper")
	}
	// The credential is still advertised (so the agent knows to request it),
	// but its value is blanked.
	val, ok := credValueFor(t, rr.Body.Bytes(), "PROD_KEY")
	if !ok {
		t.Fatalf("PROD_KEY credential entry missing from response")
	}
	if val != "" {
		t.Errorf("SECRET value must be blank under Keeper, got %q", val)
	}
}

func TestSecKeeper_SecretValuePresentWhenKeeperOff(t *testing.T) {
	h, wsID, _, agentID := covCfg2Rig(t)
	h.SetKeeperEnabled(false)
	seedAgentSecret(t, h, wsID, agentID, "ProdKey", "PROD_KEY", "SECRET")

	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	val, ok := credValueFor(t, rr.Body.Bytes(), "PROD_KEY")
	if !ok {
		t.Fatalf("PROD_KEY credential entry missing from response")
	}
	if val != withholdSecretPlaintext {
		t.Errorf("Keeper OFF: SECRET must be delivered (legacy file path), got %q", val)
	}
}

// GENERIC_SECRET is intentionally NOT withheld under Keeper (see
// docs/guides/credentials.mdx and buildKeeperBlock — the withhold set is
// exactly {SECRET}). Pin that so a future change doesn't silently broaden the
// gate and break generic-secret delivery.
func TestSecKeeper_GenericSecretStillDeliveredUnderKeeper(t *testing.T) {
	h, wsID, _, agentID := covCfg2Rig(t)
	h.SetKeeperEnabled(true)
	seedAgentSecret(t, h, wsID, agentID, "StripeHook", "STRIPE_HOOK", "GENERIC_SECRET")

	rr := covCfg2Resolve(t, h, agentID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	val, ok := credValueFor(t, rr.Body.Bytes(), "STRIPE_HOOK")
	if !ok {
		t.Fatalf("STRIPE_HOOK credential entry missing from response")
	}
	if val != withholdSecretPlaintext {
		t.Errorf("GENERIC_SECRET must still be delivered under Keeper, got %q", val)
	}
}
