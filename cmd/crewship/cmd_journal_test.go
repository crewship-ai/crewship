package main

import (
	"strings"
	"testing"
	"time"
)

func TestIsPermanentSSEError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"unauthorized", "SSE handshake: status 401", true},
		{"forbidden", "SSE handshake: status 403", true},
		{"not found", "SSE handshake: status 404", true},
		{"parse url", "parse URL: bad scheme", true},

		{"server 500", "SSE handshake: status 500", false},
		{"connection reset", "read: connection reset by peer", false},
		{"timeout", "context deadline exceeded", false},
		{"clean close", "stream closed", false},
		{"nil", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.msg != "" {
				err = errString(c.msg)
			}
			if got := isPermanentSSEError(err); got != c.want {
				t.Errorf("isPermanentSSEError(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

// errString implements error with a fixed message.
type errString string

func (e errString) Error() string { return string(e) }

// TestParseSince covers the duration suffixes (`d` is locally handled
// because Go's time.ParseDuration doesn't know about days), the
// standard duration formats, and RFC3339 fall-through. A regression in
// any branch would silently misinterpret the user's --since value.
func TestParseSince(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		in   string
		// approxAgo is the expected duration-since-now of the parsed
		// timestamp; only used for the duration-style inputs. RFC3339
		// inputs check the absolute parse.
		approxAgo time.Duration
		// absolute is the expected exact time for RFC3339-style inputs.
		absolute string
		wantErr  bool
	}{
		{name: "30 minutes", in: "30m", approxAgo: 30 * time.Minute},
		{name: "one hour", in: "1h", approxAgo: time.Hour},
		{name: "twenty-four hours", in: "24h", approxAgo: 24 * time.Hour},
		{name: "seven days", in: "7d", approxAgo: 7 * 24 * time.Hour},
		{name: "fourteen days", in: "14d", approxAgo: 14 * 24 * time.Hour},
		{name: "RFC3339 absolute", in: "2026-04-01T00:00:00Z", absolute: "2026-04-01T00:00:00Z"},
		{name: "garbage", in: "tomorrow", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSince(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if c.absolute != "" {
				want, _ := time.Parse(time.RFC3339, c.absolute)
				if !got.Equal(want) {
					t.Errorf("absolute: got %v want %v", got, want)
				}
				return
			}
			diff := now.Sub(got)
			// 5-second tolerance covers test-runner scheduling drift
			// without being so loose that "1h" could pass for "1d".
			if diff < c.approxAgo-5*time.Second || diff > c.approxAgo+5*time.Second {
				t.Errorf("approxAgo: got %v ago, want ~%v ago", diff, c.approxAgo)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than limit", "hi", 10, "hi"},
		{"equal to limit", "abcdefghij", 10, "abcdefghij"},
		{"longer than limit", "abcdefghijk", 10, "abcdefghi…"},
		{"empty", "", 5, ""},
		{"exactly limit+1", "abcdef", 5, "abcd…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateString(c.in, c.n)
			if got != c.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
			}
		})
	}
}

// TestSeverityColor pins the colour mapping a regression once split
// across two functions silently broke. Specific tokens are not asserted
// (those are private to the cli package) — instead we assert two known
// distinct severities map to different tokens.
func TestSeverityColor(t *testing.T) {
	// Each entry should map to a distinct, non-empty colour token; the
	// "unknown" branch falls through to gray (same as info).
	pairs := map[string]struct{}{}
	for _, sev := range []string{"info", "notice", "warn", "error"} {
		c := severityColor(sev)
		if c == "" {
			t.Errorf("severity %q maps to empty colour token", sev)
		}
		pairs[sev+"="+c] = struct{}{}
	}
	// info and unknown should fall through to the same default branch.
	if severityColor("unknown-foo") != severityColor("info") {
		t.Errorf("unknown severity should match info default")
	}
	// The four known severities should not all collapse into one
	// colour (warn vs error must visibly differ).
	if severityColor("warn") == severityColor("error") {
		t.Errorf("warn and error should map to distinct colours")
	}
}

// TestValidateCSV exercises the small client-side enum gate. The intent
// is that a typo on the command line surfaces as a fast, clear error
// rather than a 400 from the server.
func TestValidateCSV(t *testing.T) {
	allowed := map[string]struct{}{"a": {}, "b": {}, "c": {}}
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty is no-op", "", false},
		{"single allowed", "a", false},
		{"multiple allowed", "a,b,c", false},
		{"trim whitespace", " a , b ", false},
		{"empty segment rejected", "a,,b", true},
		{"trailing comma rejected", "a,", true},
		{"leading comma rejected", ",a", true},
		{"whitespace-only segment rejected", "a, ,b", true},
		{"single bad", "x", true},
		{"mixed bad", "a,x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateCSV("test", c.raw, allowed)
			if (err != nil) != c.wantErr {
				t.Errorf("err=%v wantErr=%v", err, c.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "--test") {
				t.Errorf("error should reference flag label: %v", err)
			}
		})
	}
}

// TestJournalCmd_Flags_Wired pins the flag set the documentation
// promises. CLI/doc drift is the loudest regression class — every flag
// added or removed needs to land here too. If a future PR hides one of
// these flags, this test fails immediately rather than the user
// discovering it through a "no such flag" error.
func TestJournalCmd_Flags_Wired(t *testing.T) {
	want := []string{
		"lines", "crew", "agent", "mission", "trace-id",
		"type", "exclude-type", "severity", "actor-type", "priority",
		"query", "since", "follow",
	}
	for _, name := range want {
		if journalCmd.Flag(name) == nil {
			t.Errorf("journal command missing --%s flag", name)
		}
	}

	// Subcommands must exist and pull their own flag set.
	subcommands := map[string][]string{
		"get":      {},
		"count":    {"crew", "type", "severity", "since", "until"},
		"priority": {"mark", "reason"},
	}
	for sub, flags := range subcommands {
		var found bool
		for _, c := range journalCmd.Commands() {
			if c.Name() == sub {
				found = true
				for _, fl := range flags {
					if c.Flag(fl) == nil {
						t.Errorf("`journal %s` missing --%s flag", sub, fl)
					}
				}
				break
			}
		}
		if !found {
			t.Errorf("missing subcommand `journal %s`", sub)
		}
	}
}

// TestJournalCmd_QueryShorthand verifies the documented `-q` shorthand
// is wired to the --query flag. Doc says `--query / -q` and shell users
// rely on the short form.
func TestJournalCmd_QueryShorthand(t *testing.T) {
	q := journalCmd.Flag("query")
	if q == nil || q.Shorthand != "q" {
		t.Errorf("--query shorthand: %v", q)
	}
}

// TestJournalGetCmd_RequiresExactlyOneArg pins cobra.ExactArgs(1) on
// `journal get`. Zero args (no entry id) and two args (typo) both
// have to fail rather than fall through to a partial request.
func TestJournalGetCmd_RequiresExactlyOneArg(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		wantOK bool
	}{
		{"zero args rejected", []string{}, false},
		{"one arg accepted", []string{"j_abc"}, true},
		{"two args rejected", []string{"j_abc", "j_def"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := journalGetCmd.Args(journalGetCmd, c.args)
			if c.wantOK && err != nil {
				t.Errorf("Args(%v) = %v, want nil", c.args, err)
			}
			if !c.wantOK && err == nil {
				t.Errorf("Args(%v) = nil, want error", c.args)
			}
		})
	}
}

// TestJournalCountCmd_RejectsPositionalArgs pins cobra.NoArgs on
// `journal count` — addressing CodeRabbit's PR #283 finding. A typo
// like `crewship journal count error` would otherwise silently run
// the unfiltered count and confuse the user.
func TestJournalCountCmd_RejectsPositionalArgs(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		wantOK bool
	}{
		{"no args accepted", []string{}, true},
		{"one positional rejected", []string{"error"}, false},
		{"two positional rejected", []string{"error", "warn"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := journalCountCmd.Args(journalCountCmd, c.args)
			if c.wantOK && err != nil {
				t.Errorf("Args(%v) = %v, want nil", c.args, err)
			}
			if !c.wantOK && err == nil {
				t.Errorf("Args(%v) = nil, want error", c.args)
			}
		})
	}
}

// TestJournalPriorityCmd_RequiresExactlyOneArg mirrors the get test —
// the priority subcommand declares ExactArgs(1) for the entry id.
func TestJournalPriorityCmd_RequiresExactlyOneArg(t *testing.T) {
	if err := journalPriorityCmd.Args(journalPriorityCmd, []string{}); err == nil {
		t.Errorf("zero args should be rejected")
	}
	if err := journalPriorityCmd.Args(journalPriorityCmd, []string{"a", "b"}); err == nil {
		t.Errorf("two args should be rejected")
	}
	if err := journalPriorityCmd.Args(journalPriorityCmd, []string{"j_abc"}); err != nil {
		t.Errorf("one arg should be accepted: %v", err)
	}
}
