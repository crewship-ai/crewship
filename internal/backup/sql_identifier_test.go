package backup

import "testing"

// sqlIdentifierRe gates every table name interpolated into PRAGMA
// queries (foreign_key_list, table_info) where the SQL driver can't
// parametrise. A future refactor that weakens the regex could open
// a PRAGMA-injection vector. This test pins the accept / reject
// behaviour so any drift fails CI before it lands.
func TestSQLIdentifierRe_Accepts(t *testing.T) {
	good := []string{
		"users",
		"workspaces",
		"agent_skills",
		"_private",
		"t",
		"T",
		"table_123",
		"abc_def_ghi",
		"UPPER_SNAKE",
	}
	for _, name := range good {
		t.Run(name, func(t *testing.T) {
			if !sqlIdentifierRe.MatchString(name) {
				t.Errorf("expected %q to match sqlIdentifierRe (legitimate SQLite identifier)", name)
			}
		})
	}
}

func TestSQLIdentifierRe_Rejects(t *testing.T) {
	bad := []string{
		"",                // empty
		"1table",          // leading digit
		"table;DROP",      // semicolon injection
		"users)",          // paren close (close PRAGMA arg)
		"users; --",       // comment injection
		"users WHERE 1=1", // WHERE injection
		"users(",          // paren open
		"users users",     // whitespace
		"users\t",         // tab
		"users\n",         // newline
		"my-table",        // hyphen (SQLite identifier rules need quoting)
		`"users"`,         // quoted identifier passed through (we quote ourselves)
		`'users'`,         // single-quoted
		"users`",          // backtick
		"user$name",       // dollar
		"x.y",             // qualified (schema.table)
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			if sqlIdentifierRe.MatchString(name) {
				t.Errorf("expected %q to be REJECTED by sqlIdentifierRe (would enable PRAGMA injection)", name)
			}
		})
	}
}
