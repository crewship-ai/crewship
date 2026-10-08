package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A vault value need not match the generic API-key regexes. Exercise the real
// credential resolver, outbound HTTP request and durable execution projections.
func TestHTTPStep_CredentialRefRedactsReflectedResponse(t *testing.T) {
	for _, injection := range []string{"bearer", "header", "query"} {
		for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
			t.Run(injection+"/"+http.StatusText(status), func(t *testing.T) {
				t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
				db := openPolicyTestDB(t)
				defer db.Close()
				if _, err := db.Exec(runsProjectionDDL); err != nil {
					t.Fatal(err)
				}
				const value = "opaque-fixture-credential-49821"
				seedCredential(t, db, "cred_echo", "ws_test", "", "API_KEY", "ACTIVE", value, "2026-10-08T00:00:00Z")
				received := make(chan string, 2)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var token string
					switch injection {
					case "bearer":
						token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					case "header":
						token = r.Header.Get("X-Fixture-Key")
					case "query":
						token = r.URL.Query().Get("key")
					}
					received <- token
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "token": token})
				}))
				defer srv.Close()

				runs := NewRunStore(db)
				emitter, ws := &captureEmitter{}, &captureWS{}
				exec := wiredHTTPExecutor(t, db).WithRunStore(runs).WithWSBroadcaster(ws)
				exec.emitter = emitter
				def := DSL{Name: "credential-echo", Agentless: true, EgressTargets: []string{"127.0.0.1"}, Steps: []Step{{
					ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL,
						CredentialRef: &CredentialRef{Type: "API_KEY", InjectAs: injection, HeaderName: "X-Fixture-Key", QueryName: "key"}},
				}}}
				body, _, _, stepErr := exec.runHTTPStep(context.Background(), def.Steps[0], RenderContext{}, RunInput{WorkspaceID: "ws_test"})
				if (stepErr != nil) != (status != http.StatusOK) {
					t.Fatalf("unexpected HTTP step error: %v", stepErr)
				}
				if strings.Contains(body, value) || !strings.Contains(body, secretRedactionMarker) {
					t.Error("HTTP response body was not redacted")
				}
				if <-received != value {
					t.Fatal("credential did not reach the upstream")
				}
				definition, err := json.Marshal(def)
				if err != nil {
					t.Fatal(err)
				}
				in := validSaveInput("credential-echo")
				in.DefinitionJSON = string(definition)
				p, err := exec.store.Save(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				result, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
				if err != nil {
					t.Fatal(err)
				}
				if <-received != value {
					t.Fatal("credential did not reach the upstream")
				}
				wantStatus := "COMPLETED"
				if status != http.StatusOK {
					wantStatus = "FAILED"
				}
				if result.Status != wantStatus {
					t.Fatalf("status = %s, want %s", result.Status, wantStatus)
				}
				record, err := runs.Get(context.Background(), result.RunID)
				if err != nil {
					t.Fatal(err)
				}
				outputs, err := runs.GetStepOutputs(context.Background(), result.RunID)
				if err != nil {
					t.Fatal(err)
				}
				surfaces := []any{result, record, outputs, emitter.entries}
				ws.mu.Lock()
				if len(ws.events) == 0 || len(emitter.entries) == 0 {
					t.Error("missing event evidence")
				}
				for _, event := range ws.events {
					surfaces = append(surfaces, event.payload)
				}
				ws.mu.Unlock()
				for _, surface := range surfaces {
					encoded, err := json.Marshal(surface)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(encoded), value) {
						t.Errorf("credential leaked through %T", surface)
					}
				}
				if status == http.StatusOK && !strings.Contains(record.Output, secretRedactionMarker) {
					t.Fatal("reflected credential must be marked as redacted")
				}
			})
		}
	}
}

func TestHTTPStep_CredentialRefRedactsEncodedValues(t *testing.T) {
	const value = "fixture+credential/with\"quotes&space here"
	for _, response := range []string{"json", "query", "request-error"} {
		t.Run(response, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch response {
				case "json":
					_ = json.NewEncoder(w).Encode(map[string]string{"token": r.URL.Query().Get("key")})
				case "query":
					_, _ = w.Write([]byte(r.URL.RawQuery))
				case "request-error":
					http.Redirect(w, r, "ftp://unsupported.example/?key="+url.QueryEscape(value), http.StatusFound)
				}
			}))
			defer srv.Close()
			exec := (&Executor{}).WithCredentialResolver(func(context.Context, RunScope, string) (string, error) { return value, nil })
			exec.SetAllowPrivateHTTPForTesting(true)
			step := Step{ID: "echo", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL,
				CredentialRef: &CredentialRef{Type: "API_KEY", InjectAs: "query", QueryName: "key"}}}
			output, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{}, RunInput{})
			if (err != nil) != (response == "request-error") {
				t.Fatalf("unexpected error: %v", err)
			}
			if err != nil {
				output += err.Error()
			}
			encoded, _ := json.Marshal(value)
			for _, representation := range []string{value, string(encoded[1 : len(encoded)-1]), url.QueryEscape(value)} {
				if strings.Contains(output, representation) {
					t.Error("encoded credential leaked")
				}
			}
			if !strings.Contains(output, secretRedactionMarker) {
				t.Error("expected a redaction marker")
			}
		})
	}
}
