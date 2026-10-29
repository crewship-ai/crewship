package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCraftCLIInitializeWordFileKeepsAdmissionPrivateAndUsesIssuedLease(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("b", 64)
	native := []byte(`{"native":"word-model"}`)
	file := filepath.Join(t.TempDir(), "document.json")
	if err := os.WriteFile(file, native, 0600); err != nil {
		t.Fatal(err)
	}
	stub.OnPost(base+"/initialize", func(r *http.Request, raw []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != "" {
			t.Error("initializer sent an existing admission")
		}
		var body struct {
			Application string `json:"application_id"`
			Bytes       []byte `json:"bytes"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Application != "wordcraft" || !bytes.Equal(body.Bytes, native) {
			t.Error("initializer changed Word native bytes or app")
		}
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"new-public-session","application_id":"wordcraft","page_id":"page-1","generation":"9007199254740993","version":"1"}`), "application/json"
	})
	stub.OnPost(base+"/lease/release", func(r *http.Request, raw []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret || !strings.Contains(string(raw), `"generation":"9007199254740993"`) {
			t.Error("release lost issued private admission/string generation")
		}
		return 200, []byte(`{"holder":null}`), "application/json"
	})
	out, err := runPageCLI(t, `{"action":"release","generation":"9007199254740993"}`+"\n", "craft", "initialize", "health", "--file", file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "session_token") || !strings.Contains(out, `"version":"1"`) {
		t.Fatal("private token leaked or initial public receipt absent")
	}
	if len(stub.CallsFor("POST", base+"/initialize")) != 1 || len(stub.CallsFor("POST", base+"/sessions")) != 0 || len(stub.CallsFor("POST", base+"/lease")) != 0 {
		t.Fatal("initialize must use its single issued admission, without implicit acquire")
	}
}

func TestCraftCLIInitializeExplicitApplicationAndBinaryBytes(t *testing.T) {
	for _, app := range []string{"wordcraft", "gridcraft", "deckcraft"} {
		t.Run(app, func(t *testing.T) {
			stub := pageStub(t)
			native := []byte("{\"native\":true}")
			if app == "deckcraft" {
				native = []byte{'P', 'K', 3, 4, 0, 255, 128, 0, 10}
			}
			file := filepath.Join(t.TempDir(), "native-document")
			if err := os.WriteFile(file, native, 0600); err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/pages/health/craft/initialize"
			secret := strings.Repeat("c", 64)
			stub.OnPost(path, func(r *http.Request, raw []byte) (int, []byte, string) {
				var body struct {
					Application string `json:"application_id"`
					Bytes       []byte `json:"bytes"`
				}
				if json.Unmarshal(raw, &body) != nil || body.Application != app || !bytes.Equal(body.Bytes, native) {
					t.Error("selected app or raw native bytes changed")
				}
				return 201, []byte(fmt.Sprintf(`{"session_token":%q,"session_id":"explicit-session","application_id":%q,"page_id":"p","generation":"1","version":"1"}`, secret, app)), "application/json"
			})
			out, err := runPageCLI(t, "", "craft", "initialize", "health", "--application", app, "--file", file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, app) || strings.Contains(out, secret) || len(stub.CallsFor("POST", path)) != 1 {
				t.Fatal("initialization receipt/app/private admission mismatch")
			}
		})
	}
}
func TestCraftCLIInitializeRejectsMismatchedApplicationResponse(t *testing.T) {
	stub := pageStub(t)
	file := filepath.Join(t.TempDir(), "deckcraft")
	if err := os.WriteFile(file, []byte{'P', 'K', 3, 4, 255}, 0600); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/pages/health/craft/initialize"
	secret := strings.Repeat("d", 64)
	stub.OnPost(path, func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"mismatched-session","application_id":"wordcraft","page_id":"p","generation":"1","version":"1"}`), "application/json"
	})
	out, err := runPageCLI(t, `{"action":"release","generation":"1"}`+"\n", "craft", "initialize", "health", "--application", "deckcraft", "--file", file)
	if err == nil || len(stub.CallsFor("POST", path)) != 1 || len(stub.CallsFor("POST", "/api/v1/pages/health/craft/lease/release")) != 0 || strings.Contains(out, secret) || strings.Contains(out, "mismatched-session") {
		t.Fatal("wrong app response exposed or used its admission")
	}
}
func TestCraftCLIInitializeRejectsUnknownApplicationBeforeRequest(t *testing.T) {
	stub := pageStub(t)
	_, err := runPageCLI(t, "{}", "craft", "initialize", "health", "--application", "othercraft", "--file", "-")
	if err == nil || len(stub.CallsFor("POST", "/api/v1/pages/health/craft/initialize")) != 0 {
		t.Fatal("unknown application reached initializer")
	}
}
