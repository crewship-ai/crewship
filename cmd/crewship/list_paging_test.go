package main

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestSetListPaging_SendsOnlyWhatIsSet(t *testing.T) {
	p := url.Values{}
	setListPaging(p, 0, 0)
	if p.Encode() != "" {
		t.Fatalf("zero limit/offset must send nothing (server default); got %q", p.Encode())
	}
	setListPaging(p, 25, 50)
	if p.Get("limit") != "25" || p.Get("offset") != "50" {
		t.Fatalf("limit/offset not encoded: %v", p)
	}
}

func TestReadListMeta_ReadsHeadersAndKnowsWhenAbsent(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	if m := readListMeta(resp); m.Known {
		t.Fatal("no X-Total-Count must read as unknown, not as zero")
	}
	resp.Header.Set("X-Total-Count", "1015")
	resp.Header.Set("X-Limit", "50")
	resp.Header.Set("X-Offset", "100")
	m := readListMeta(resp)
	if !m.Known || m.Total != 1015 || m.Limit != 50 || m.Offset != 100 {
		t.Fatalf("meta = %+v, want total 1015 limit 50 offset 100", m)
	}
	if m := readListMeta(nil); m.Known {
		t.Fatal("nil response must read as unknown")
	}
}

func footerOutput(t *testing.T, format string, meta listMeta, shown int) string {
	t.Helper()
	var buf bytes.Buffer
	f := cli.NewFormatter(format)
	f.Writer = &buf
	printListFooter(f, meta, shown)
	return buf.String()
}

func TestPrintListFooter(t *testing.T) {
	full := listMeta{Total: 1015, Limit: 50, Offset: 0, Known: true}
	cases := []struct {
		name   string
		format string
		meta   listMeta
		shown  int
		want   string
	}{
		{"first page names the next offset", "table", full, 50, "showing 1–50 of 1015 · next page: --offset 50\n"},
		{"middle page adds the offset", "table", listMeta{Total: 1015, Limit: 50, Offset: 100, Known: true}, 50, "showing 101–150 of 1015 · next page: --offset 150\n"},
		{"last page offers no next", "table", listMeta{Total: 7, Limit: 3, Offset: 6, Known: true}, 1, "showing 7–7 of 7\n"},
		{"an offset past the end says so", "table", listMeta{Total: 3, Limit: 50, Offset: 500, Known: true}, 0, "no rows at offset 500 · the list has 3\n"},
		{"everything shown says nothing", "table", listMeta{Total: 3, Limit: 50, Offset: 0, Known: true}, 3, ""},
		{"unknown total says nothing", "table", listMeta{}, 50, ""},
		{"json stays clean", "json", full, 50, ""},
		{"ndjson stays clean", "ndjson", full, 50, ""},
		{"quiet is for scripts", "quiet", full, 50, ""},
	}
	for _, tc := range cases {
		if got := footerOutput(t, tc.format, tc.meta, tc.shown); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
