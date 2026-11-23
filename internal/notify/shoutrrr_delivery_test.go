package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

func TestShoutrrrWebhookRedirectsAndHTTPStatus(t *testing.T) {
	var crossHostRequests atomic.Int32
	var sameHostPosts atomic.Int32
	var crossHostTarget string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/kept", http.StatusTemporaryRedirect)
		case "/kept":
			if r.Method == http.MethodPost {
				sameHostPosts.Add(1)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/cross":
			http.Redirect(w, r, crossHostTarget, http.StatusTemporaryRedirect)
		case "/target":
			crossHostRequests.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	crossHostTarget = strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/target"

	provider := shoutrrrProvider{}
	for _, tc := range []struct {
		path      string
		wantError bool
	}{
		{path: "/same"},
		{path: "/cross", wantError: true},
		{path: "/refused", wantError: true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			err := provider.Send(context.Background(), "generic+"+server.URL+tc.path, "test message", nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("send returned %v, want error %t", err, tc.wantError)
			}
		})
	}
	if posts := sameHostPosts.Load(); posts != 1 {
		t.Errorf("same-host redirects preserved POST %d times, want 1", posts)
	}
	if requests := crossHostRequests.Load(); requests != 0 {
		t.Errorf("cross-host target received %d requests", requests)
	}
}

type localDiscordTransport struct {
	target    *url.URL
	transport *http.Transport
}

func (d localDiscordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	local := req.Clone(req.Context())
	local.URL.Scheme = d.target.Scheme
	local.URL.Host = d.target.Host
	local.Host = d.target.Host
	return d.transport.RoundTrip(local)
}

func TestShoutrrrDiscordRequiresExplicitMentions(t *testing.T) {
	type payload struct {
		AllowedMentions *struct {
			Parse []string
			Users []string
			Roles []string
		} `json:"allowed_mentions"`
	}
	for _, tc := range []struct {
		name     string
		query    string
		wantUser string
	}{
		{name: "body text only"},
		{name: "explicit user", query: "?mentions=user%3A123456789012345679", wantUser: "123456789012345679"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan payload, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body payload
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode Discord payload: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				received <- body
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			r, err := router.NewWithOptions(nil, types.SenderOptions{
				HTTPClient: &http.Client{Transport: localDiscordTransport{target, server.Client().Transport.(*http.Transport)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			service, err := r.Locate("discord://test-token@123456789012345678" + tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Send("@everyone <@123456789012345680>", nil); err != nil {
				t.Fatal(err)
			}
			select {
			case body := <-received:
				mentions := body.AllowedMentions
				if mentions == nil || len(mentions.Parse) != 0 || len(mentions.Roles) != 0 || strings.Join(mentions.Users, ",") != tc.wantUser {
					t.Fatalf("unexpected allowed mentions: %+v", mentions)
				}
			default:
				t.Fatal("Discord payload never reached the loopback server")
			}
		})
	}
}
