package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrewConfigBodyBoundedAndComplete(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"normal", `{"name":"crew"}`, true},
		{"trailing JSON", `{} {}`, false},
		{"trailing garbage", "{}garbage", false},
		{"over budget", "{}" + strings.Repeat(" ", 8<<20), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			request.ContentLength = -1
			var decoded map[string]any
			err := readCrewConfigJSON(request, &decoded)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
