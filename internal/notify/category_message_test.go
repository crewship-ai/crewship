package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testSecret = "sk-ant-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// DeliverCategoryMessage scrubbed msg.Body and nothing else.
//
// The one caller that noticed — the agent-send handler — worked around it by
// scrubbing the title itself, with a comment saying the delivery path "was
// never asked to" cover that field. That left the OTHER three producers
// (journal bridge, inbox router, recovery sweep) delivering titles straight
// from a journal summary or an inbox card to Discord, unscrubbed.
//
// A workaround at one of four call sites is the shape of the bug, not the
// fix: scrubbing is a property of delivering a message, so it belongs to the
// message, once, where every producer passes through.

func TestDeliverCategoryMessage_ScrubsEveryAuthoredField(t *testing.T) {
	fp := &fakeProvider{}
	defer SetProviderForTesting(fp)()

	d := fastDispatcher(t, staticLister{}, nil)
	ch := Channel{ID: "c1", Type: ChannelShoutrrr, Secret: "slack://hook:T@webhook", Enabled: true}

	err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w",
		Category:    CategoryRoutinesFailed,
		Title:       "Run failed using " + testSecret,
		Body:        "the key was " + testSecret,
		Links:       []Link{{Label: "Open " + testSecret, Path: "/runs/run_1"}},
	})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	calls := fp.calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 send, got %d", len(calls))
	}
	if strings.Contains(calls[0].Message, testSecret) {
		t.Errorf("a secret reached the channel unscrubbed:\n%s", calls[0].Message)
	}
	// Body and link label travel as message text; the title now travels as a
	// native param. Both routes leave the instance, so both are checked here
	// — a title that stopped being scrubbed when it moved would be the same
	// leak in a place nobody was looking.
	if got := strings.Count(calls[0].Message, "[REDACTED"); got != 2 {
		t.Errorf("want body and link label redacted (2 markers), got %d:\n%s",
			got, calls[0].Message)
	}
	title := calls[0].Params["title"]
	if strings.Contains(title, testSecret) {
		t.Errorf("a secret reached the channel through the title param: %q", title)
	}
	if !strings.Contains(title, "[REDACTED") {
		t.Errorf("title param was not scrubbed: %q", title)
	}
}

func TestDeliverCategoryMessage_ScrubsVarsIncludingNested(t *testing.T) {
	// Vars is the fact bag templates render against, so anything a producer
	// puts there can end up in a message body. It is the widest leak surface
	// the envelope has, and a producer copying a source payload in wholesale
	// is the expected usage, not the exotic one.
	var (
		mu   sync.Mutex
		body []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := fastDispatcher(t, staticLister{}, nil)
	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "topsecret", Enabled: true}

	err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w", Category: CategoryRoutinesFailed, Title: "Run failed",
		Vars: map[string]any{
			"run_id": "run_1",
			"env":    map[string]any{"ANTHROPIC_API_KEY": testSecret},
			"args":   []any{"--key", testSecret},
		},
	})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(string(body), testSecret) {
		t.Errorf("a secret nested in Vars reached the webhook:\n%s", body)
	}
}

// scrubValue's type switch handled string, map[string]any and []any, and
// returned everything else untouched. Producer payloads are Go values, not
// decoded JSON, so a concretely-typed one walks straight through: lookout
// writes Payload["findings"] = result.Findings ([]lookout.Finding), the
// journal bridge copies it into Vars verbatim, and deliverCategoryWebhook
// marshals it — including the excerpt of the offending prompt each finding
// carries — into the security webhook's `vars`.
//
// The body and title of the same message ARE redacted, so nothing about the
// delivered text shows the leak; only the JSON carries it.
func TestDeliverCategoryMessage_ScrubsTypedPayloadValues(t *testing.T) {
	type finding struct {
		Kind    string   `json:"kind"`
		Matched string   `json:"matched"`
		Tags    []string `json:"tags"`
	}
	var (
		mu   sync.Mutex
		body []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := fastDispatcher(t, staticLister{}, nil)
	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "topsecret", Enabled: true}

	err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w", Category: CategorySecurity, Title: "Guardrail hit",
		Vars: map[string]any{
			// A slice of structs, a typed string slice and a typed map —
			// the shapes a Go producer actually builds.
			"findings":  []finding{{Kind: "prompt_injection", Matched: "leak " + testSecret, Tags: []string{testSecret}}},
			"hosts":     []string{"a.test", testSecret},
			"headers":   map[string]string{"authorization": testSecret},
			"attempts":  3,
			"truncated": true,
		},
	})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(string(body), testSecret) {
		t.Errorf("a typed value carried a secret into the webhook payload:\n%s", body)
	}
	// Non-string values must survive as themselves — redaction is not
	// permission to flatten the payload.
	for _, want := range []string{`"attempts":3`, `"truncated":true`, `"a.test"`, `"prompt_injection"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("payload lost %s:\n%s", want, body)
		}
	}
}

func TestDeliverCategoryMessage_ShoutrrrCarriesAbsoluteLinks(t *testing.T) {
	fp := &fakeProvider{}
	defer SetProviderForTesting(fp)()

	d := fastDispatcher(t, staticLister{}, nil).WithPublicURL("https://crewship.example.com")
	ch := Channel{ID: "c1", Type: ChannelShoutrrr, Secret: "slack://hook:T@webhook", Enabled: true}

	err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w", Category: CategoryRoutinesFailed, Title: "Run failed",
		Links: []Link{
			{Label: "Open issue", Path: "/issues/CS-12"},
			{Label: "View journal", Path: "/journal?entry=je_1"},
		},
	})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	msg := fp.calls()[0].Message
	for _, want := range []string{
		"https://crewship.example.com/issues/CS-12",
		"https://crewship.example.com/journal?entry=je_1",
		"Open issue",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
	// Every link, not just the primary one — the whole point of a list.
	if strings.Count(msg, "https://crewship.example.com") != 2 {
		t.Errorf("want both links delivered, got:\n%s", msg)
	}
}

func TestDeliverCategoryMessage_WebhookCarriesLinksAndKeepsURL(t *testing.T) {
	// `url` is the field an existing webhook receiver already parses. It has
	// to keep meaning what it meant — the primary link — while `links`
	// carries the full set alongside it.
	var (
		mu   sync.Mutex
		body []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := fastDispatcher(t, staticLister{}, nil).WithPublicURL("https://crewship.example.com")
	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "topsecret", Enabled: true}

	err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w", Category: CategoryIssuesAssigned, Title: "Issue assigned",
		Links: []Link{
			{Label: "Open issue", Path: "/issues/CS-12"},
			{Label: "View journal", Path: "/journal?entry=je_1"},
		},
		Vars: map[string]any{"issue_identifier": "CS-12"},
	})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var p struct {
		URL   string `json:"url"`
		Links []struct {
			Label string `json:"label"`
			URL   string `json:"url"`
		} `json:"links"`
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p.URL != "https://crewship.example.com/issues/CS-12" {
		t.Errorf("url = %q, want the primary link, absolute", p.URL)
	}
	if len(p.Links) != 2 || p.Links[1].URL != "https://crewship.example.com/journal?entry=je_1" {
		t.Errorf("links = %+v, want both, absolute", p.Links)
	}
	if p.Vars["issue_identifier"] != "CS-12" {
		t.Errorf("vars did not survive: %+v", p.Vars)
	}
}

func TestDeliverCategoryMessage_EmailListsLinks(t *testing.T) {
	fm := &fakeMailer{}
	d := fastDispatcher(t, staticLister{}, fm).WithPublicURL("https://crewship.example.com")
	ch := Channel{ID: "c1", Type: ChannelEmail, To: "ops@example.com", Enabled: true}

	err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w", Category: CategoryIssuesAssigned, Title: "Issue assigned",
		Links: []Link{{Label: "Open issue", Path: "/issues/CS-12"}},
	})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(fm.sent) != 1 {
		t.Fatalf("want 1 e-mail, got %d", len(fm.sent))
	}
	if !strings.Contains(fm.sent[0].Text, "https://crewship.example.com/issues/CS-12") {
		t.Errorf("e-mail text has no absolute link:\n%s", fm.sent[0].Text)
	}
}
