package main

// Acceptance for `crewship inbox get` naming what an escalation asks for,
// driven through the BUILT BINARY: the LINK's URL and the CREDENTIAL's name
// travel in the item payload (internal/api/escalation_handler.go) and the
// detail view prints each on its own labelled line, above the body — the
// Context dump at the bottom is a key/value list a reader should not have to
// mine for the one fact that decides the verdict.

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func inboxGetStub(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/inbox/ibx_1":
			_, _ = w.Write([]byte(`{"id":"ibx_1","kind":"escalation","state":"unread","priority":"high","blocking":true,
				"title":"Agent escalation: need write access","body_md":"Raised while working on task 35",
				"sender_type":"agent","sender_name":"robin","created_at":"2026-09-03T13:12:00Z","payload":` + payload + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_InboxGet_PrintsTheLinkOnItsOwnLine(t *testing.T) {
	srv := inboxGetStub(t, `{"escalation_type":"LINK","link_url":"https://github.com/crewship-ai/crewship/settings/access","crew_id":"c1","chat_id":"ch1"}`)
	out, err := runChatListCLI(t, srv.URL, "inbox", "get", "ibx_1")
	if err != nil {
		t.Fatalf("inbox get: %v\noutput: %s", err, out)
	}
	if !regexp.MustCompile(`(?m)^link\s+https://github\.com/crewship-ai/crewship/settings/access$`).MatchString(out) {
		t.Errorf("no labelled link line:\n%s", out)
	}
}

func TestAcceptance_InboxGet_PrintsTheCredentialName(t *testing.T) {
	srv := inboxGetStub(t, `{"escalation_type":"CREDENTIAL","credential_name":"STRIPE_TEST_KEY","has_pending_credential":true,"credential_id":"cred_1"}`)
	out, err := runChatListCLI(t, srv.URL, "inbox", "get", "ibx_1")
	if err != nil {
		t.Fatalf("inbox get: %v\noutput: %s", err, out)
	}
	if !regexp.MustCompile(`(?m)^credential\s+STRIPE_TEST_KEY$`).MatchString(out) {
		t.Errorf("no labelled credential line:\n%s", out)
	}
}

func TestAcceptance_InboxGet_NoLabelWhenThePayloadHasNone(t *testing.T) {
	srv := inboxGetStub(t, `{"escalation_type":"TEXT","crew_id":"c1"}`)
	out, err := runChatListCLI(t, srv.URL, "inbox", "get", "ibx_1")
	if err != nil {
		t.Fatalf("inbox get: %v\noutput: %s", err, out)
	}
	if regexp.MustCompile(`(?m)^(link|credential)\s`).MatchString(out) {
		t.Errorf("a TEXT question printed a link or credential line:\n%s", out)
	}
}
