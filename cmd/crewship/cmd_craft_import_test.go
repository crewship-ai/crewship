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

func TestCraftCLIImportConvertsOnTheServerThenInitializesWithItsDocument(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/book/craft"
	secret := strings.Repeat("c", 64)
	native := []byte(`{"sheets":[{"name":"S"}]}`)
	xlsx := []byte("PK\x03\x04 pretend workbook")
	file := filepath.Join(t.TempDir(), "book.xlsx")
	if err := os.WriteFile(file, xlsx, 0o600); err != nil {
		t.Fatal(err)
	}
	stub.OnPost(base+"/import", func(r *http.Request, raw []byte) (int, []byte, string) {
		if !bytes.Equal(raw, xlsx) {
			t.Error("the workbook was changed on its way to the server")
		}
		return 202, []byte(`{"id":"job1","state":"converting"}`), "application/json"
	})
	stub.OnGet(base+"/import/job1", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"id":"job1","state":"ready","cells":3,"archive":{"notes":["macros are not imported"]}}`), "application/json"
	})
	stub.OnGet(base+"/import/job1/document", func(*http.Request, []byte) (int, []byte, string) {
		return 200, native, "application/json"
	})
	stub.OnPost(base+"/initialize", func(r *http.Request, raw []byte) (int, []byte, string) {
		var body struct {
			Application string `json:"application_id"`
			Bytes       []byte `json:"bytes"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Application != "gridcraft" || !bytes.Equal(body.Bytes, native) {
			t.Error("initialize did not get the converted native document")
		}
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"new-session","application_id":"gridcraft","page_id":"page-1","generation":"1","version":"1"}`), "application/json"
	})
	stub.OnPost(base+"/lease/release", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"holder":null}`), "application/json"
	})
	out, err := runPageCLI(t, `{"action":"release","generation":"1"}`+"\n", "craft", "import", "book", "--file", file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || !strings.Contains(out, `"version":"1"`) {
		t.Fatalf("output: %s", out)
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, xlsx) {
		t.Fatal("the original workbook file was modified")
	}
}

func TestCraftCLIImportRefusalIsExplainedAndNothingIsInitialized(t *testing.T) {
	for code, want := range map[string]string{"truncated": "only part of this workbook", "not_xlsx": "not a readable .xlsx", "too_large": "import limits"} {
		t.Run(code, func(t *testing.T) {
			stub := pageStub(t)
			base := "/api/v1/pages/book/craft"
			file := filepath.Join(t.TempDir(), "book.xlsx")
			_ = os.WriteFile(file, []byte("PK"), 0o600)
			stub.OnPost(base+"/import", func(*http.Request, []byte) (int, []byte, string) {
				return 202, []byte(`{"id":"j","state":"converting"}`), "application/json"
			})
			stub.OnGet(base+"/import/j", func(*http.Request, []byte) (int, []byte, string) {
				return 200, []byte(`{"id":"j","state":"failed","code":"` + code + `"}`), "application/json"
			})
			_, err := runPageCLI(t, "", "craft", "import", "book", "--file", file)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error: %v", err)
			}
			if len(stub.CallsFor("POST", base+"/initialize")) != 0 {
				t.Fatal("a refused import initialized the page")
			}
		})
	}
}

func TestCraftCLIImportRefusesAnUnreadableOrEmptyFileBeforeAnyRequest(t *testing.T) {
	stub := pageStub(t)
	empty := filepath.Join(t.TempDir(), "empty.xlsx")
	_ = os.WriteFile(empty, nil, 0o600)
	for _, file := range []string{filepath.Join(t.TempDir(), "missing.xlsx"), empty} {
		if _, err := runPageCLI(t, "", "craft", "import", "book", "--file", file); err == nil {
			t.Errorf("%s accepted", file)
		}
	}
	if len(stub.Calls()) != 0 {
		t.Fatal("a request was sent for an unreadable file")
	}
}
