package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestCraftNativePageMetadataNamesExplicitProfile(t *testing.T) {
	_, m, ctx := craftNativeHTTPFixture(t)
	for _, method := range []string{"get", "list"} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/pages/native-page", nil).WithContext(ctx)
			req.SetPathValue("slug", "native-page")
			out := httptest.NewRecorder()
			if method == "get" {
				m.pages.Get(out, req)
			} else {
				m.pages.List(out, req)
			}
			if out.Code != 200 {
				t.Fatalf("metadata %d %s", out.Code, out.Body.String())
			}
			var value map[string]any
			if method == "get" {
				if err := json.Unmarshal(out.Body.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
			} else {
				var list []map[string]any
				if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
					t.Fatal(err)
				}
				for _, row := range list {
					if row["id"] == "native-page" {
						value = row
					}
				}
			}
			if value["native_craft_application"] != "wordcraft" {
				t.Fatalf("missing native descriptor: %s", out.Body.String())
			}
		})
	}
}
