package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A model id outside every catalog must be storable by hand: the catalog is a
// list of suggestions, not an inventory of what an account can run (#5376).
func TestAgentUpdate_CustomModelIDAccepted(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"ANTHROPIC", "claude-future-9-9"},
		{"ANTHROPIC", "claude-opus-5-5[1m]"},
		{"OPENAI", "gpt-7-preview"},
		{"GOOGLE", "gemini-5-pro-exp"},
		{"OLLAMA", "qwen4-coder:32b"},
		{"OPENCODE", "openrouter/anthropic/claude-sonnet-5.5"},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			h, userID, wsID := covAUHandler(t)
			seedAgentRow(t, h.db, "ag-custom", wsID, "", "Mira", "mira", "AGENT")
			body, _ := json.Marshal(map[string]string{"llm_provider": tc.provider, "llm_model": tc.model})
			rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-custom", string(body))
			if rr.Code != http.StatusOK {
				t.Fatalf("custom model update = %d, want 200 (%s)", rr.Code, rr.Body.String())
			}
			var got string
			if err := h.db.QueryRow(`SELECT llm_model FROM agents WHERE id = 'ag-custom'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.model {
				t.Fatalf("stored llm_model = %q, want %q", got, tc.model)
			}
		})
	}
}

// Changing only the model keeps the stored provider and still accepts an id
// the provider's catalog does not list.
func TestAgentUpdate_CustomModelWithoutProvider(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-db", wsID, "", "Otto", "otto", "AGENT")
	if _, err := h.db.Exec(`UPDATE agents SET llm_provider = 'OPENAI' WHERE id = 'ag-db'`); err != nil {
		t.Fatalf("set provider: %v", err)
	}
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-db", `{"llm_model":"gpt-6.2-sol"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("custom model (provider from DB) = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
}

func TestAgentUpdate_MalformedModelIDRejected(t *testing.T) {
	for name, model := range map[string]string{
		"space":     "claude sonnet",
		"leading":   " gpt-6.1-sol",
		"newline":   "gpt-6.1-sol\n--yolo",
		"shell":     "gpt;rm -rf",
		"quote":     `gpt"6`,
		"dollar":    "$(id)",
		"non-ascii": "gpt-6·sol",
		"too long":  strings.Repeat("a", maxLLMModelIDLen+1),
	} {
		t.Run(name, func(t *testing.T) {
			h, userID, wsID := covAUHandler(t)
			seedAgentRow(t, h.db, "ag-bad", wsID, "", "Bad", "bad", "AGENT")
			body, _ := json.Marshal(map[string]string{"llm_provider": "OPENAI", "llm_model": model})
			rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-bad", string(body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("malformed model %q = %d, want 400 (%s)", model, rr.Code, rr.Body.String())
			}
			var got string
			_ = h.db.QueryRow(`SELECT COALESCE(llm_model,'') FROM agents WHERE id = 'ag-bad'`).Scan(&got)
			if got == model {
				t.Fatal("malformed model id was stored")
			}
		})
	}
}

func TestAgentUpdate_EmptyModelClears(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-clear", wsID, "", "Cleo", "cleo", "AGENT")
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-clear", `{"llm_model":""}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty model = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
}
