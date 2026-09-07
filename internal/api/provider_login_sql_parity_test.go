package api

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// List filtering and the credential-ID guard must classify legacy rows the
// same way; otherwise a row hidden by its detail endpoint leaks in a list.
func TestProviderLoginSQLMatchesAccountPolicy(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	filter, args := loginRowsSQL()
	providers := append(providerlogin.Providers(), "STRIPE", "NONE", "")
	for _, typ := range []string{CredTypeProviderLogin, CredTypeAPIKey, CredTypeAICLIToken, CredTypeSecret} {
		for _, provider := range providers {
			for _, padding := range []string{"", " ", "\t\r\n", "\u00a0\u2003\u3000"} {
				value := padding + strings.ToLower(provider) + padding
				var count int
				params := append([]any{typ, value}, args...)
				if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS type, ? AS provider) c WHERE 1=1`+filter, params...).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if got, want := count == 1, isLoginRow(typ, value); got != want {
					t.Errorf("type=%q provider=%q: SQL classifies login=%v, account policy=%v", typ, value, got, want)
				}
			}
		}
	}
}
