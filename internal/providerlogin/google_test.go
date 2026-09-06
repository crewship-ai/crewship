package providerlogin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGoogleLoginRefresh(t *testing.T) {
	l, err := Split("GOOGLE", "", `{"access_token":"access-old","refresh_token":"refresh-private","expiry_date":1800000000000,"scope":"custom-scope"}`)
	if err != nil || l.Mode != ModeSubscription || l.Scope != "custom-scope" || !l.RefreshSupported() {
		t.Fatalf("import failed: %+v %v", l, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Error(err)
		}
		if req.Form.Get("refresh_token") != l.RefreshToken || req.Form.Get("client_secret") == "" || req.Form.Get("grant_type") != "refresh_token" {
			t.Error("incomplete refresh request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-new","expires_in":3600}`))
	}))
	defer server.Close()
	r := NewGoogleRefresher(server.Client())
	r.TokenURL = server.URL
	before := time.Now()
	result, err := r.Refresh(context.Background(), l.RefreshToken)
	if err != nil || result.AccessToken != "access-new" || result.RefreshToken != "" || result.ExpiresAt.Before(before.Add(59*time.Minute)) {
		t.Fatalf("bad refresh result: %+v %v", result, err)
	}
	if Due(result.ExpiresAt, before, RefreshLeadFor("GOOGLE")) {
		t.Fatal("fresh Google token immediately due")
	}
}
