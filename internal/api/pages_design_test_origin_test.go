package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDesignCreationRetainsExplicitTestOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		marked     bool
	}{
		{"default", `{"slug":"board","name":"Board"}`, false},
		{"explicit false", `{"slug":"board","name":"Board","created_by_test":false}`, false},
		{"explicit true", `{"slug":"board","name":"Board","created_by_test":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, workspace, owner, call := designFixture(t)
			if w := call(owner, "create", tc.body); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			rec, err := h.loadPage(t.Context(), workspace, "board")
			if err != nil || rec.CreatedByTest != tc.marked {
				t.Fatalf("persisted origin %#v %v", rec, err)
			}
			for _, filter := range []string{"", "?test_items=include", "?test_items=only"} {
				request := pagesRequest(t, "GET", "/api/v1/pages"+filter, workspace, owner, "OWNER", "")
				out := httptest.NewRecorder()
				h.List(out, request)
				var listed []pageListWire
				want := 1
				if filter == "" && tc.marked || filter == "?test_items=only" && !tc.marked {
					want = 0
				}
				if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &listed) != nil || len(listed) != want {
					t.Fatalf("catalog %s %d %s", filter, out.Code, out.Body)
				}
				if len(listed) > 0 && listed[0].CreatedByTest != tc.marked {
					t.Fatalf("catalog marker %s %#v", filter, listed[0])
				}
			}
			if w := call(owner, "apply", `{"created_by_test":false,"operations":[]}`); w.Code != 400 {
				t.Fatalf("later operations accepted marker %d %s", w.Code, w.Body)
			}
			rec, err = h.loadPage(t.Context(), workspace, "board")
			if err != nil || rec.CreatedByTest != tc.marked {
				t.Fatalf("later request changed origin %#v %v", rec, err)
			}
		})
	}
}
