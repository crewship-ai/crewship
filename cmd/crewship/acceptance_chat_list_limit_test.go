package main

// Acceptance for `crewship chat list --limit`, driven through the BUILT BINARY.
//
// Same hazard as --kind: a limit is worth exactly nothing unless it reaches
// the server, because the server is where the page is cut. The stub asserts
// on the raw query string.

import (
	"strings"
	"testing"
)

func TestAcceptance_ChatList_SendsLimitToTheServer(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--limit", "5")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); !strings.Contains(q, "limit=5") {
		t.Errorf("query = %q, want it to carry limit=5", q)
	}
}

func TestAcceptance_ChatList_LimitComposesWithKind(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--kind", "direct", "--limit", "3")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	q := stub.lastQuery(t)
	if !strings.Contains(q, "kind=direct") || !strings.Contains(q, "limit=3") {
		t.Errorf("query = %q, want both kind=direct and limit=3", q)
	}
}

func TestAcceptance_ChatList_NoLimitWhenNotAsked(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); strings.Contains(q, "limit") {
		t.Errorf("query = %q, want no limit parameter (the server's default is the default)", q)
	}
}

func TestAcceptance_ChatList_SendsOffsetAndPrintsTheFooter(t *testing.T) {
	stub := &chatListStub{body: chatListFixture, total: 19}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--limit", "2", "--offset", "2")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	q := stub.lastQuery(t)
	if !strings.Contains(q, "limit=2") || !strings.Contains(q, "offset=2") {
		t.Errorf("query = %q, want limit=2 and offset=2", q)
	}
	// The footer says what the table did not show — a page that fills the
	// terminal looks complete otherwise.
	if !strings.Contains(out, "showing 3–4 of 19") {
		t.Errorf("no paging footer:\n%s", out)
	}
}
