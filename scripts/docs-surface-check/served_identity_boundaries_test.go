package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServedCheckDoesNotConfusePageCountsWithPageIdentity(t *testing.T) {
	for _, links := range []string{
		"- [One](https://example.test/one.md)\n- [Duplicate one](https://example.test/one.md)\n",
		"- [One](https://example.test/one.md)\n- [Unrelated](https://example.test/unrelated.md)\n",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/llms.txt" {
				_, _ = w.Write([]byte(links))
				return
			}
			_, _ = w.Write([]byte("full index content"))
		}))
		count, err := checkServed(server.URL, []string{"one", "guides/required"})
		server.Close()
		if count != 2 || err == nil || !strings.Contains(err.Error(), "guides/required") {
			t.Fatalf("missing page hidden by matching counts: count=%d err=%v", count, err)
		}
	}
}
