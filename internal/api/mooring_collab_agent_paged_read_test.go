package api

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMooringAgentPageQueryBindsWorkspaceAndExactRevision(t *testing.T) {
	cursor := `["host-read","stable&?/Č😀"]`
	query := url.Values{"workspace_id": {"ws"}, "page": {"1"}, "cursor": {cursor}, "revision": {"18446744073709551615"}, "max_bytes": {"4096"}}
	args, err := collaborationPageQuery(httptest.NewRequest("GET", "/?"+query.Encode(), nil), "ws")
	if err != nil || args == nil || args.cursor == nil || *args.cursor != cursor || args.revision == nil || string(*args.revision) != "18446744073709551615" || args.limit != 4096 {
		t.Fatal("lossless bounded continuation refused")
	}
	for _, query := range []string{"", "workspace_id=ws"} {
		args, err := collaborationPageQuery(httptest.NewRequest("GET", "/?"+query, nil), "ws")
		if args != nil || err != nil {
			t.Fatal("legacy whole-document read changed")
		}
	}
	for _, query := range []string{
		"page=1&workspace_id=other", "page=1&actor=other", "page=1&page=1",
		"page=1&cursor=x", "page=1&cursor=&revision=0", "page=1&revision=00",
		"page=1&revision=18446744073709551616", "page=1&max_bytes=1023", "page=1&max_bytes=262145",
		"page=1&max_bytes=04096", "page=1&revision=%", "cursor=x&revision=0", "page=2",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.URL.RawQuery = query
		if _, err := collaborationPageQuery(r, "ws"); err == nil {
			t.Errorf("invalid or unbound read accepted: %q", query)
		}
	}
}

func TestMooringAbandonedReadDiscardsPayloadWithoutFreeingUncertainCorrelation(t *testing.T) {
	p := &collaborationAgentPageRead{raw: []byte("private native document"), ready: make(chan struct{})}
	a := &collaborationLiveAgent{pageRead: p}
	a.abandonPageRead(p)
	if a.pageRead != p || !p.abandoned || p.done || p.raw != nil {
		t.Fatal("uncertain correlation freed or private payload retained")
	}
	next := &collaborationAgentPageRead{raw: []byte("next private native document")}
	a.pageRead = next
	a.abandonPageRead(p)
	if a.pageRead != next || next.abandoned || next.raw == nil {
		t.Fatal("late abandoned caller changed the next read")
	}
}
