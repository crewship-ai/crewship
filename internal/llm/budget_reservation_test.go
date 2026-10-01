package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/lookout"
)

func codecBudgetDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openLLMTestDB(t)
	raw, e := os.ReadFile("../database/migrations/20260930083100_restricted_cost_reservations.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(raw)); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode)VALUES('cap','ws','workspace','ws','month',2,'hard')`); e != nil {
		t.Fatal(e)
	}
	return db
}
func TestHardBudgetActualCodecsRetainUnknownAndSettleKnown(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		for _, stream := range []bool{false, true} {
			for _, kind := range []string{"known", "missing", "duplicate", "negative", "fractional", "overbound", "repeat_usage", "redirect", "error"} {
				t.Run(provider+map[bool]string{false: "/complete/", true: "/stream/"}[stream]+kind, func(t *testing.T) {
					db := codecBudgetDB(t)
					calls := 0
					usage := `"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":20}}`
					if provider == "anthropic" {
						usage = `"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30}`
					}
					if kind == "missing" {
						usage = `"usage":{}`
					}
					if kind == "duplicate" {
						usage += `,` + usage
					}
					if kind == "negative" {
						usage = strings.ReplaceAll(usage, ":100", ":-1")
					}
					if kind == "fractional" {
						usage = strings.ReplaceAll(usage, ":100", ":1.5")
					}
					if kind == "overbound" {
						usage = strings.ReplaceAll(usage, ":10", ":17")
					}
					client := &http.Client{Transport: rtFunc(func(request *http.Request) (*http.Response, error) {
						calls++
						var body map[string]any
						if e := json.NewDecoder(request.Body).Decode(&body); e != nil {
							t.Fatal(e)
						}
						if body["service_tier"] != nil || body["model"] == nil {
							t.Fatal("unbounded request")
						}
						responseBody := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` + usage + `}`
						if provider == "anthropic" {
							responseBody = `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` + usage + `}`
						}
						if stream && provider == "openai" {
							responseBody = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[]," + usage + "}\n\ndata: [DONE]\n\n"
						}
						if stream && provider == "anthropic" {
							responseBody = "data: {\"type\":\"message_start\",\"message\":{" + usage + "}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":10}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
						}
						if kind == "overbound" && stream && provider == "anthropic" {
							responseBody = strings.ReplaceAll(responseBody, `"output_tokens":10`, `"output_tokens":17`)
						}
						if kind == "repeat_usage" {
							if stream && provider == "openai" {
								responseBody = strings.Replace(responseBody, "data: [DONE]", "data: {\"choices\":[],"+usage+"}\n\ndata: [DONE]", 1)
							}
							if stream && provider == "anthropic" {
								responseBody += "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":10}}\n\n"
							}
						}
						status := 200
						header := make(http.Header)
						if kind == "redirect" {
							status = 307
							header.Set("Location", "https://foreign.invalid/charge")
						}
						if kind == "error" {
							status = 503
							responseBody = "provider may have charged"
						}
						return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
					})}
					var base Provider
					model := "gpt-5-mini"
					if provider == "openai" {
						base = NewOpenAICompat(OpenAICompatConfig{APIKey: "sk-synthetic-bounded", Client: client, StreamClient: client, MaxTokensField: "max_completion_tokens", IncludeUsage: true})
					} else {
						model = "claude-haiku-4-5"
						base = NewAnthropicWith(AnthropicConfig{APIKey: "sk-ant-synthetic-bounded", Client: client})
					}
					wrapper := Middleware(base, nil, db)
					ctx := lookout.WithScope(context.Background(), lookout.Scope{WorkspaceID: "ws"})
					request := Request{Model: model, MaxTokens: 16, Messages: []Message{{Role: RoleUser, Content: "hello"}}}
					var response *Response
					var e error
					if stream {
						response, e = wrapper.Stream(ctx, request, func(StreamEvent) error { return nil })
					} else {
						response, e = wrapper.Complete(ctx, request)
					}
					if (kind == "error" || kind == "redirect" || kind == "fractional" && !stream) != (e != nil) {
						t.Fatalf("response=%+v error=%v", response, e)
					}
					if calls != 1 {
						t.Fatalf("hard-capped hidden retries=%d", calls)
					}
					var state string
					var max, cost float64
					var count int
					if e = db.QueryRow(`SELECT r.state,r.reserved_usd,l.cost_usd FROM restricted_cost_reservations r JOIN cost_ledger l ON l.id=r.ledger_id`).Scan(&state, &max, &cost); e != nil {
						t.Fatal(e)
					}
					if e = db.QueryRow(`SELECT COUNT(*) FROM cost_ledger`).Scan(&count); e != nil || count != 1 {
						t.Fatal("duplicate ledger", e, count)
					}
					if kind == "known" || kind == "repeat_usage" && !stream {
						if state != "known" || cost <= 0 || cost >= max || response == nil || !response.UsageKnown {
							t.Fatalf("known state=%s cost=%v max=%v response=%+v", state, cost, max, response)
						}
						var fresh, cached, created int
						if e = db.QueryRow(`SELECT input_tokens,cached_input_tokens,cache_creation_tokens FROM cost_ledger`).Scan(&fresh, &cached, &created); e != nil {
							t.Fatal(e)
						}
						wantFresh, wantCreated := 80, 0
						if provider == "anthropic" {
							wantFresh, wantCreated = 100, 30
						}
						if fresh != wantFresh || cached != 20 || created != wantCreated {
							t.Fatalf("codec channels fresh=%d cached=%d creation=%d", fresh, cached, created)
						}
					} else if state != "unknown" || cost != max {
						t.Fatalf("unknown state=%s cost=%v max=%v", state, cost, max)
					}
				})
			}
		}
	}
}
func TestHardBudgetProviderContractRejectsOverrides(t *testing.T) {
	for _, kind := range []string{"extra_body", "endpoint", "token_field", "auth", "subscription", "anthropic_beta"} {
		t.Run(kind, func(t *testing.T) {
			db := codecBudgetDB(t)
			called := false
			client := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, io.EOF })}
			config := OpenAICompatConfig{APIKey: "sk-synthetic-bounded", Client: client, MaxTokensField: "max_completion_tokens"}
			switch kind {
			case "extra_body":
				config.ExtraBody = map[string]any{"service_tier": "priority", "model": "foreign", "max_completion_tokens": 999999}
			case "endpoint":
				config.BaseURL = "https://foreign.invalid/v1"
			case "token_field":
				config.MaxTokensField = "ignored_output_cap"
			case "auth":
				config.NoAuth = true
			case "subscription":
				config.APIKey = "synthetic.jwt.subscription"
			}
			var base Provider = NewOpenAICompat(config)
			model := "gpt-5-mini"
			if kind == "anthropic_beta" {
				base = NewAnthropicWith(AnthropicConfig{APIKey: "sk-ant-synthetic-bounded", Client: client, Beta: []string{"unbounded-provider-beta"}})
				model = "claude-haiku-4-5"
			}
			ctx := lookout.WithScope(context.Background(), lookout.Scope{WorkspaceID: "ws"})
			if _, e := Middleware(base, nil, db).Complete(ctx, Request{Model: model, MaxTokens: 1}); e == nil || called {
				t.Fatalf("unbounded contract reached network called=%v error=%v", called, e)
			}
		})
	}
}
