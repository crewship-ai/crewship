package main

import (
	"net/http"
	"testing"
)

func TestPageAssetsCLIRequests(t *testing.T) {
	for _, tc := range []struct {
		args                                []string
		method, path, input, response, mime string
	}{
		{[]string{"upload", "report", "--file", "-", "--mutation-id", "retry-1"}, "POST", "/api/v1/pages/report/assets", "raster-bytes", `{"id":"asset1","state":"staged"}`, "application/json"},
		{[]string{"show", "report", "asset1"}, "GET", "/api/v1/pages/report/assets/asset1", "", `{"id":"asset1","state":"staged"}`, "application/json"},
		{[]string{"download", "report", "asset1"}, "GET", "/api/v1/pages/report/assets/asset1/bytes", "", "canonical-raster-bytes", "image/png"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			stub := pageStub(t)
			handler := func(r *http.Request, raw []byte) (int, []byte, string) {
				if tc.method == "POST" {
					if string(raw) != tc.input || r.URL.Query().Get("mutation_id") != "retry-1" || r.Header.Get("Content-Type") != "application/octet-stream" {
						t.Errorf("wrong upload %s %s", r.URL, raw)
					}
				}
				return 200, []byte(tc.response), tc.mime
			}
			if tc.method == "POST" {
				stub.OnPost(tc.path, handler)
			} else {
				stub.OnGet(tc.path, handler)
			}
			old := findSubcommand(pageCmd.Commands(), "assets")
			fresh := newPageAssetsCommand()
			pageCmd.RemoveCommand(old)
			pageCmd.AddCommand(fresh)
			t.Cleanup(func() { pageCmd.RemoveCommand(fresh); pageCmd.AddCommand(old) })
			out, err := runPageCLI(t, tc.input, append([]string{"page", "assets"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if len(stub.CallsFor(tc.method, tc.path)) != 1 {
				t.Fatal("wrong route")
			}
			if tc.args[0] == "download" && out != tc.response {
				t.Fatalf("bytes modified: %q", out)
			}
		})
	}
}
