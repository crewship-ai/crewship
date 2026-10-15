package config

import "testing"

func TestInternalAPIURLUsesBoundListener(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"", "http://127.0.0.1:8085"},
		{"0.0.0.0", "http://127.0.0.1:8085"},
		{"127.0.0.2", "http://127.0.0.2:8085"},
		{"::", "http://[::1]:8085"},
		{"::1", "http://[::1]:8085"},
		{"[::1]", "http://[::1]:8085"},
		{"192.0.2.7", "http://192.0.2.7:8085"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			cfg := Default()
			cfg.Server.Host, cfg.Server.Port = tc.host, 8085
			cfg.Auth.NextjsURL = "https://browser.example"
			if got := cfg.InternalAPIURL(); got != tc.want {
				t.Fatalf("internal listener URL = %q, want %q", got, tc.want)
			}
		})
	}
}
