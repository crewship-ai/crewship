package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SlashCommand is one user-defined command loaded from
// ~/.crewship/commands/<name>.md.
//
// The file format mirrors the pattern that OpenCode / Claude Code use:
// a YAML frontmatter block (between two `---` lines) declares metadata,
// the body becomes the prompt template. Placeholders use `$VAR` or
// `${VAR}` substitution — values come from positional CLI args by
// declaration order.
type SlashCommand struct {
	// Name is derived from the filename (basename without .md). It's
	// the cobra subcommand name the user types after `crewship`.
	Name string

	// Description is shown in --help.
	Description string

	// Agent overrides the default agent for this command. Empty falls
	// through to the user's default-agent config.
	Agent string

	// Effort optionally pre-fills --effort for this command.
	Effort string

	// Plan, when true, runs this command in plan-mode automatically.
	Plan bool

	// Vars lists positional argument names in declaration order. The
	// CLI binds args[0] → Vars[0], etc. Extra args beyond the count
	// are joined into the last variable.
	Vars []string

	// Body is the prompt template with $VAR / ${VAR} substitutions.
	Body string

	// Source is the file path the command was loaded from (useful for
	// debugging / `crewship slash list`).
	Source string
}

// SlashFrontmatter is the YAML shape we parse from the frontmatter block.
// Unknown keys are ignored so users can stash their own annotations.
type slashFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Agent       string   `yaml:"agent"`
	Effort      string   `yaml:"effort"`
	Plan        bool     `yaml:"plan"`
	Vars        []string `yaml:"vars"`
}

// DefaultSlashDir returns the canonical commands directory (~/.crewship/commands).
func DefaultSlashDir() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "commands"), nil
}

// LoadSlashCommands walks the slash-commands directory and returns one
// SlashCommand per *.md file. Missing directory → empty slice + nil
// (slash commands are an opt-in surface; absence is not an error).
//
// The ctx parameter is honoured between per-file parses so a slow
// network-mounted commands directory can be aborted by CLI shutdown
// without leaving the directory walk stuck.
//
// Each file is parsed independently; one malformed file warns to
// stderr but does not abort the rest, so a single bad command doesn't
// break the whole CLI.
func LoadSlashCommands(ctx context.Context) ([]SlashCommand, error) {
	dir, err := DefaultSlashDir()
	if err != nil {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read slash dir %s: %w", dir, err)
	}
	out := make([]SlashCommand, 0, len(entries))
	for _, e := range entries {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			default:
			}
		}
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		full := filepath.Join(dir, name)
		sc, err := ParseSlashFile(full)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s[slash]%s skipping %s: %v\n", Yellow, Reset, full, err)
			continue
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ParseSlashFile reads a single slash-command .md file.
func ParseSlashFile(path string) (SlashCommand, error) {
	f, err := os.Open(path)
	if err != nil {
		return SlashCommand{}, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	return parseSlashReader(f, path)
}

// parseSlashReader is the testable form — accepts an io.Reader so unit
// tests can use bytes.NewBufferString without filesystem fixtures.
func parseSlashReader(r io.Reader, source string) (SlashCommand, error) {
	br := bufio.NewReader(r)
	first, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return SlashCommand{}, fmt.Errorf("read: %w", err)
	}
	first = strings.TrimSpace(first)

	var fm slashFrontmatter
	var bodyBuf strings.Builder

	if first == "---" {
		// Collect frontmatter until the closing `---`.
		var fmBuf strings.Builder
		for {
			line, err := br.ReadString('\n')
			if strings.TrimRight(line, "\r\n") == "---" {
				break
			}
			fmBuf.WriteString(line)
			if err != nil {
				if err == io.EOF {
					return SlashCommand{}, fmt.Errorf("frontmatter not terminated by ---")
				}
				return SlashCommand{}, fmt.Errorf("read frontmatter: %w", err)
			}
		}
		if err := yaml.Unmarshal([]byte(fmBuf.String()), &fm); err != nil {
			return SlashCommand{}, fmt.Errorf("parse frontmatter: %w", err)
		}
	} else {
		// No frontmatter — `first` is part of the body.
		bodyBuf.WriteString(first)
		bodyBuf.WriteString("\n")
	}

	// Body = rest of the file. A read failure here means the file is
	// being truncated mid-load (rare but seen on network mounts) —
	// surface it rather than silently writing a partial body that
	// would then be saved as a slash command.
	rest, err := io.ReadAll(br)
	if err != nil {
		return SlashCommand{}, fmt.Errorf("read body of %s: %w", source, err)
	}
	bodyBuf.Write(rest)

	name := fm.Name
	if name == "" {
		base := filepath.Base(source)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if !slashNameValid(name) {
		return SlashCommand{}, fmt.Errorf("invalid command name %q (allowed: a-z 0-9 -)", name)
	}
	return SlashCommand{
		Name:        name,
		Description: fm.Description,
		Agent:       fm.Agent,
		Effort:      fm.Effort,
		Plan:        fm.Plan,
		Vars:        fm.Vars,
		Body:        strings.TrimSpace(bodyBuf.String()),
		Source:      source,
	}, nil
}

// slashNameRE constrains command names to safe shell tokens. cobra would
// accept anything with `Use:` but a name like "foo bar" or "rm -rf"
// would surprise users and break completion.
var slashNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func slashNameValid(s string) bool {
	return slashNameRE.MatchString(s)
}

// Render substitutes $VAR / ${VAR} placeholders in c.Body with values
// from args. Mapping: c.Vars[i] = args[i]. Extra args (i > len(Vars))
// are joined with spaces and appended to the last declared var; if no
// Vars are declared, all args are joined and substituted under the
// implicit name `$args` for ergonomic prompts like "Summarise $args".
//
// Unsubstituted placeholders are left as-is so the agent at least sees
// a plain-text marker if a var was forgotten.
//
// Substitution iterates placeholders sorted by name length, longest
// first. Without that order, a shorter var name that is a prefix of a
// longer one (e.g. `a` vs `args`) would let `$a` greedily match inside
// `$args` and corrupt the output. Map iteration in Go is randomised,
// so the bug used to surface intermittently.
func (c SlashCommand) Render(args []string) string {
	out := c.Body
	values := map[string]string{}
	if len(c.Vars) == 0 {
		values["args"] = strings.Join(args, " ")
	} else {
		for i, v := range c.Vars {
			if i < len(args) {
				values[v] = args[i]
			} else {
				values[v] = ""
			}
		}
		// Spill extras into the last declared var, joined.
		if len(args) > len(c.Vars) {
			last := c.Vars[len(c.Vars)-1]
			values[last] = values[last] + " " + strings.Join(args[len(c.Vars):], " ")
			values[last] = strings.TrimSpace(values[last])
		}
	}
	// Sort keys by descending length so `$args` is substituted before
	// `$a` — otherwise `$a` would chew off the leading two characters
	// of any `$args` token before the longer name even gets a chance.
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })

	// Replace ${VAR} first so $VARNAME doesn't gobble adjacent text.
	for _, k := range keys {
		out = strings.ReplaceAll(out, "${"+k+"}", values[k])
	}
	for _, k := range keys {
		out = strings.ReplaceAll(out, "$"+k, values[k])
	}
	return out
}
