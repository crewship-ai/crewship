package main

import (
	"bytes"
	"encoding/json"
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
