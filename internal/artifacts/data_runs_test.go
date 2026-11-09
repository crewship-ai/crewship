package artifacts

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanDataRunReason(t *testing.T) {
	for name, tc := range map[string]struct {
		in      string
		keep    []string
		without []string
	}{
		"plain":           {in: "  upstream timed out after 30s  ", keep: []string{"upstream timed out after 30s"}},
		"bearer":          {in: "Authorization: Bearer abc.def-123", keep: []string{"Authorization"}, without: []string{"abc.def-123"}},
		"basic":           {in: "basic dXNlcjpwYXNz rejected", keep: []string{"rejected"}, without: []string{"dXNlcjpwYXNz"}},
		"key value":       {in: "password=hunter2 api_key: zz9 cookie=sid=1", without: []string{"hunter2", "zz9", "sid=1"}},
		"crewship token":  {in: "used crewship_cli_0123456789abcdef", without: []string{"0123456789abcdef"}},
		"provider token":  {in: "sk-proj_abcdefghijkl ghp_ABCDEFGHIJ12", without: []string{"abcdefghijkl", "ABCDEFGHIJ12"}},
		"url userinfo":    {in: "GET https://bot:s3cret@db.example/x failed", keep: []string{"db.example/x failed"}, without: []string{"s3cret", "bot:"}},
		"control":         {in: "a\x00b\x1b[31mc​d\r\ne", keep: []string{"ab[31mcd\ne"}},
		"invalid utf8":    {in: "bad \xff byte", keep: []string{"bad ? byte"}},
		"bounded":         {in: strings.Repeat("ž", 400), keep: []string{"žž"}},
		"no false secret": {in: "token bucket empty", keep: []string{"token bucket empty"}},
		"json password":   {in: `body {"password":"hunter2","user":"bot"}`, keep: []string{`"user":"bot"`}, without: []string{"hunter2"}},
		"json spaced":     {in: `{"api_key" : "zz9-top"}`, without: []string{"zz9-top"}},
		"access token":    {in: "refresh failed: access_token=abc123secret&x=1", without: []string{"abc123secret"}},
		"client secret":   {in: "client_secret=s3cr3tvalue", without: []string{"s3cr3tvalue"}},
		"env password":    {in: "PGPASSWORD=pgpass99 psql failed", keep: []string{"psql failed"}, without: []string{"pgpass99"}},
		"quoted value":    {in: `password = "hunter 2" rejected`, keep: []string{"rejected"}, without: []string{"hunter", " 2\""}},
		"single quoted":   {in: `secret: 'two words' end`, keep: []string{"end"}, without: []string{"two words"}},
		"jwt":             {in: "got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJlX3Rlc3Q from idp", keep: []string{"from idp"}, without: []string{"eyJhbGciOiJIUzI1NiJ9", "c2lnbmF0dXJlX3Rlc3Q"}},
		"aws key":         {in: "denied for AKIAIOSFODNN7EXAMPLE in us-east-1", keep: []string{"us-east-1"}, without: []string{"AKIAIOSFODNN7EXAMPLE"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := CleanDataRunReason(tc.in)
			if len(got) > MaxDataRunReason || !utf8.ValidString(got) {
				t.Fatalf("unbounded or invalid: %d bytes", len(got))
			}
			for _, want := range tc.keep {
				if !strings.Contains(got, want) {
					t.Errorf("%q lost %q", got, want)
				}
			}
			for _, secret := range tc.without {
				if strings.Contains(got, secret) {
					t.Errorf("%q kept %q", got, secret)
				}
			}
		})
	}
}
