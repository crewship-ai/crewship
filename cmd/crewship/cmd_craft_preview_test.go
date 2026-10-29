package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCraftCLIPreviewDoesNotOpenEditorAdmission(t *testing.T) {
	stub := pageStub(t)
	path := "/api/v1/pages/health/craft/preview"
	stub.OnGet(path, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != "" {
			t.Error("preview sent editor admission")
		}
		return 200, []byte(`{"version":"9007199254740993","bytes":"e30=","applicationId":"wordcraft"}`), "application/json"
	})
	out, err := runPageCLI(t, "", "craft", "preview", "health")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "9007199254740993") || !strings.Contains(out, "e30=") {
		t.Fatal("preview did not preserve native version/bytes")
	}
	if len(stub.CallsFor("GET", path)) != 1 || len(stub.CallsFor("POST", "/api/v1/pages/health/craft/sessions")) != 0 {
		t.Fatal("preview opened custody or repeated its read")
	}
}
func TestCraftCLIShareBoundExistingVersionAndNoMutationReplay(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("a", 64)
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"native-preview-session","application_id":"wordcraft"}`), "application/json"
	})
	stub.OnPost(base+"/document/share", func(r *http.Request, raw []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret {
			t.Error("share lost opaque admission")
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || len(body) != 8 || body["sessionId"] != "native-preview-session" || body["generation"] != "9007199254740995" || body["baseVersion"] != "9007199254740993" || body["targetVersion"] != "9007199254740993" {
			t.Error("share binding changed")
		}
		return 403, []byte(`{"error":"denied"}`), "application/json"
	})
	snapshot := `{"workspaceId":"` + covWSCli3 + `","pageId":"p","applicationId":"wordcraft","enginePin":"` + strings.Repeat("b", 64) + `","sessionId":"native-preview-session","generation":"9007199254740995","baseVersion":"9007199254740993","targetVersion":"9007199254740993"}`
	out, err := runPageCLI(t, `{"action":"share","snapshot":`+snapshot+`}`+"\n", "craft", "connect", "health")
	if err == nil {
		t.Fatal("refused share reported success")
	}
	if strings.Contains(out, secret) || strings.Contains(out, "session_token") {
		t.Fatal("private admission leaked")
	}
	if len(stub.CallsFor("POST", base+"/document/share")) != 1 {
		t.Fatal("share not sent once")
	}
}

func TestCraftCLISharedPreviewRedirectsNeverReplay(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, operation := range []string{"preview", "share"} {
			t.Run(fmt.Sprintf("%s-%d", operation, status), func(t *testing.T) {
				pageStub(t)
				var original, target atomic.Int32
				secret := strings.Repeat("c", 64)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/pages/health/craft/sessions" {
						w.WriteHeader(201)
						_, _ = w.Write([]byte(`{"session_token":"` + secret + `","session_id":"redirect-session","application_id":"wordcraft"}`))
						return
					}
					expected := "/api/v1/pages/health/craft/preview"
					if operation == "share" {
						expected = "/api/v1/pages/health/craft/document/share"
					}
					if r.URL.Path == expected {
						original.Add(1)
						if operation == "share" && r.Header.Get("X-Craft-Session") != secret {
							t.Error("share missing private admission")
						}
						if operation == "preview" && r.Header.Get("X-Craft-Session") != "" {
							t.Error("preview carried admission")
						}
						w.Header().Set("Location", "/must-not-follow")
						w.WriteHeader(status)
						return
					}
					target.Add(1)
					_, _ = w.Write([]byte(`{"version":"2","bytes":"e30=","phase":"shared","durability":"node"}`))
				}))
				defer server.Close()
				t.Setenv("CREWSHIP_SERVER", server.URL)
				t.Setenv("CREWSHIP_TOKEN", "qa-redirect-human-token")
				var out string
				var err error
				if operation == "preview" {
					out, err = runPageCLI(t, "", "craft", "preview", "health")
				} else {
					snapshot := `{"workspaceId":"` + covWSCli3 + `","pageId":"p","applicationId":"wordcraft","enginePin":"` + strings.Repeat("b", 64) + `","sessionId":"redirect-session","generation":"1","baseVersion":"1","targetVersion":"1"}`
					out, err = runPageCLI(t, `{"action":"share","snapshot":`+snapshot+`}`+"\n", "craft", "connect", "health")
				}
				if strings.Contains(out, secret) || strings.Contains(out, "session_token") {
					t.Fatal("private admission leaked")
				}
				if err == nil || original.Load() != 1 || target.Load() != 0 {
					t.Fatalf("redirect was followed: error=%v original=%d target=%d", err != nil, original.Load(), target.Load())
				}
			})
		}
	}
}
