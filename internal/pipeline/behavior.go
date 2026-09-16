package pipeline

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Behavior describes declared rules using this engine's semantics. It is not
// an audit of a historical run's permissions, tool calls or actual verdicts.
type Behavior struct {
	Steps []StepBehavior `json:"steps"`
	Cost  string         `json:"cost"`
	Scope string         `json:"scope"`
}
type StepBehavior struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Performer string   `json:"performer"`
	Checks    []string `json:"checks"`
	Failure   string   `json:"failure"`
	Attempts  string   `json:"attempts"`
	Timeout   string   `json:"timeout"`
}

// outcomeTierLimit is also used by runAgentStep. Zero preserves legacy tier
// traversal; a positive limit caps it, never creates extra model invocations.
func outcomeTierLimit(step Step, available int) int {
	if step.Outcomes != nil && step.Outcomes.MaxIterations > 0 && step.Outcomes.MaxIterations < available {
		return step.Outcomes.MaxIterations
	}
	return available
}

func DescribeBehavior(dsl *DSL) *Behavior {
	if dsl == nil {
		return nil
	}
	out := &Behavior{Steps: []StepBehavior{}, Cost: "No recipe cost cap declared. Workspace and parent-run budgets may still apply.", Scope: "Recipe rules only. Runtime permissions and actions chosen by agents or called routines are checked separately. These are configured checks, not proof that a run passed them."}
	if dsl.MaxCostUSD > 0 {
		out.Cost = fmt.Sprintf("Recipe cost cap: $%g. Checked at execution boundaries; work already performed cannot be refunded. Stricter parent or workspace limits may apply.", dsl.MaxCostUSD)
	}
	var walk func([]Step, string)
	walk = func(steps []Step, prefix string) {
		for _, step := range steps {
			s := StepBehavior{ID: prefix + step.ID, Name: step.Name, Performer: string(step.Type), Checks: []string{}, Failure: "An execution error fails the run after configured recovery is exhausted.", Attempts: "No step-level execution retry configured. Agent transport recovery and model fallbacks are separate.", Timeout: "No step timeout declared; runner and run limits still apply."}
			if s.Name == "" {
				s.Name = step.ID
			}
			if step.AgentSlug != "" {
				s.Performer = "Agent: " + step.AgentSlug
			}
			if step.Type == StepCallPipeline {
				s.Performer = "Routine: " + step.PipelineSlug
			}
			if step.Type == StepWait && step.Wait != nil {
				s.Performer = "Wait for " + step.Wait.Kind
			}
			if step.TimeoutSec > 0 {
				s.Timeout = fmt.Sprintf("Declared step timeout: %d seconds.", step.TimeoutSec)
			}
			rp := step.Retry
			if rp == nil && step.OnFail == OnFailRetryStep {
				rp = defaultRetryPolicy()
			}
			if rp != nil {
				s.Attempts = fmt.Sprintf("Up to %d step execution attempts, including the first. Cancellation is not retried; model fallbacks and transport recovery are separate.", min(rp.MaxAttempts, retryMaxAttemptsCeiling))
			}
			v := step.Validation
			if v != nil {
				if len(v.Schema) > 0 {
					s.Checks = append(s.Checks, "JSON output schema")
				}
				if v.MinLength != nil {
					s.Checks = append(s.Checks, fmt.Sprintf("Minimum output length: %d bytes", *v.MinLength))
				}
				if v.MaxLength != nil {
					s.Checks = append(s.Checks, fmt.Sprintf("Maximum output length: %d bytes", *v.MaxLength))
				}
				if len(v.MustContain) > 0 {
					s.Checks = append(s.Checks, fmt.Sprintf("%d required text checks", len(v.MustContain)))
				}
				if len(v.MustNotContain) > 0 {
					s.Checks = append(s.Checks, fmt.Sprintf("%d forbidden text checks", len(v.MustNotContain)))
				}
			}
			if len(s.Checks) > 0 && step.Type != StepAgentRun {
				s.Checks = append(s.Checks, "Declared structural checks are not enforced by this live step runner. A fixture test is not a live output gate.")
			}
			if step.Type == StepAgentRun {
				action := step.OnFail
				if action == "" || action == OnFailRetryStep {
					action = OnFailEscalateTier
				}
				if action == OnFailAbort {
					s.Failure = "A structural check failure stops this step."
				} else {
					s.Failure = "A structural check failure tries the next configured model tier with feedback; exhaustion fails the step."
				}
			}
			if step.Outcomes != nil && step.Type != StepAgentRun {
				s.Checks = append(s.Checks, "Declared checker outcomes are not enforced by this live step runner.")
			}
			if o := step.Outcomes; o != nil && step.Type == StepAgentRun {
				mode := "Advisory availability: an unavailable checker does not block the result"
				if o.Required {
					mode = "Required: an unavailable checker blocks the result"
				}
				s.Checks = append(s.Checks, fmt.Sprintf("Checker %s: %d criteria. %s.", o.GraderAgentSlug, len(o.Criteria), mode))
				for _, c := range o.Criteria {
					s.Checks = append(s.Checks, c.Name+": "+c.Rule)
				}
				if outcomesOnFail(step) == OnFailAbort {
					s.Failure += " A rejected checker verdict fails the step."
				} else {
					s.Failure += " A rejected checker verdict tries the next configured model tier with feedback."
				}
				if o.MaxIterations > 0 {
					s.Attempts += fmt.Sprintf(" At most %d worker/checker model tiers per execution attempt; stops earlier if no fallback remains.", o.MaxIterations)
				}
			}
			if len(s.Checks) == 0 {
				s.Checks = append(s.Checks, "No output acceptance checks declared. Successful execution alone does not establish result quality.")
			}
			out.Steps = append(out.Steps, s)
			if step.Foreach != nil {
				walk(step.Foreach.Steps, prefix+step.ID+"/")
			}
		}
	}
	walk(dsl.Steps, "")
	return out
}

// FileRef is one file a routine runs or reads, projected from the recipe
// alone: DescribeFiles never touches a volume. Presence, size, timestamp and
// the header-comment description are filled in by the API layer from the
// author crew's shared volume, best-effort; a file that cannot be reached
// stays `present:false` with the size and time omitted.
//
// Path is relative to /crew/shared — the form authors write in
// `script.path` — whichever spelling the recipe used.
type FileRef struct {
	Path        string   `json:"path"`
	Language    string   `json:"language"`
	Interpreter string   `json:"interpreter"`
	StepIDs     []string `json:"step_ids"`
	Description string   `json:"description"`
	SizeBytes   *int64   `json:"size_bytes,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	Present     bool     `json:"present"`
	// Status says what the share check established: "present" (listed on
	// the crew share), "missing" (the share was listed and the file is not
	// there) or "unverified" (the share could not be listed, the per-request
	// I/O budget ran out first, or no crew owns the routine). Present is the
	// boolean shorthand of "present" and stays for older readers.
	Status string `json:"status"`
}

const (
	FileStatusPresent    = "present"
	FileStatusMissing    = "missing"
	FileStatusUnverified = "unverified"
)

// fileLanguages is the extension set the UI has an icon and a label for.
// The language is the extension token itself, so an unknown extension is "".
var fileLanguages = map[string]bool{
	"go": true, "py": true, "ts": true, "js": true, "sh": true, "bash": true,
	"yaml": true, "yml": true, "json": true, "md": true, "sql": true,
}

// sharedFileArgPattern finds file-looking paths under the crew share inside
// script args / env values. The last segment must carry an extension so a
// directory argument ("/crew/shared/data") is not reported as a file.
var sharedFileArgPattern = regexp.MustCompile(`/crew/shared/[A-Za-z0-9_./-]*[A-Za-z0-9_-]\.[A-Za-z0-9]+`)

// DescribeFiles lists the files a recipe runs, in recipe order: every
// `script.path` first (walking routine hooks, step hooks and foreach bodies),
// then file paths under /crew/shared found in `script.args` / `script.env`
// values. One row per distinct path; StepIDs accumulates every step that
// names it, nested foreach steps as "parent/child" like DescribeBehavior.
// Never nil — the wire contract is `files: []` when nothing is declared.
func DescribeFiles(dsl *DSL) []FileRef {
	out := []FileRef{}
	if dsl == nil {
		return out
	}
	type scriptRef struct {
		stepID string
		script *ScriptStep
	}
	var scripts []scriptRef
	var walk func(steps []Step, prefix string)
	visit := func(step *Step, prefix string) {
		if step == nil {
			return
		}
		if step.Hooks != nil && step.Hooks.Before != nil && step.Hooks.Before.Script != nil {
			scripts = append(scripts, scriptRef{prefix + step.Hooks.Before.ID, step.Hooks.Before.Script})
		}
		if step.Script != nil {
			scripts = append(scripts, scriptRef{prefix + step.ID, step.Script})
		}
		if step.Foreach != nil {
			walk(step.Foreach.Steps, prefix+step.ID+"/")
		}
		if step.Hooks != nil && step.Hooks.After != nil && step.Hooks.After.Script != nil {
			scripts = append(scripts, scriptRef{prefix + step.Hooks.After.ID, step.Hooks.After.Script})
		}
	}
	walk = func(steps []Step, prefix string) {
		for i := range steps {
			visit(&steps[i], prefix)
		}
	}
	if dsl.Hooks != nil {
		visit(dsl.Hooks.BeforeAll, "")
	}
	walk(dsl.Steps, "")
	if dsl.Hooks != nil {
		visit(dsl.Hooks.AfterAll, "")
		visit(dsl.Hooks.OnFailure, "")
	}

	index := map[string]int{}
	add := func(rel, stepID, explicitInterpreter string) {
		if i, ok := index[rel]; ok {
			if !slices.Contains(out[i].StepIDs, stepID) {
				out[i].StepIDs = append(out[i].StepIDs, stepID)
			}
			return
		}
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(rel)), ".")
		ref := FileRef{Path: rel, StepIDs: []string{stepID}, Interpreter: strings.TrimSpace(explicitInterpreter), Status: FileStatusUnverified}
		if fileLanguages[ext] {
			ref.Language = ext
		}
		if ref.Interpreter == "" {
			ref.Interpreter = scriptInterpreterByExt["."+ext]
		}
		index[rel] = len(out)
		out = append(out, ref)
	}
	for _, s := range scripts {
		if rel, ok := sharedRelativePath(s.script.Path); ok {
			add(rel, s.stepID, s.script.Interpreter)
		}
	}
	for _, s := range scripts {
		values := append([]string{}, s.script.Args...)
		for _, k := range slices.Sorted(maps.Keys(s.script.Env)) {
			values = append(values, s.script.Env[k])
		}
		for _, v := range values {
			for _, m := range sharedFileArgPattern.FindAllString(v, -1) {
				if rel, ok := sharedRelativePath(m); ok {
					add(rel, s.stepID, "")
				}
			}
		}
	}
	return out
}

// sharedRelativePath mirrors resolveScriptPath's fence and returns the path
// relative to /crew/shared. A path that escapes the share (traversal, an
// absolute path elsewhere, the root itself) is not a file the routine can
// run, so it is not a file to report.
func sharedRelativePath(p string) (string, bool) {
	abs, err := resolveScriptPath(p)
	if err != nil {
		return "", false
	}
	rel := strings.TrimPrefix(abs, scriptSharedRoot)
	if rel == "" {
		return "", false
	}
	return rel, true
}

// fileDescriptionMaxChars caps the header comment the Files card shows.
const fileDescriptionMaxChars = 200

// FileDescription returns the leading comment of a source file — the header
// paragraph an author wrote to say what the file does — flattened to one
// line of at most 200 characters. Recognises `//`, `#`, `--`, `/* */` and
// `<!-- -->`; a shebang, leading blank lines and pure decoration lines
// (`# =====`) are skipped; the first blank line or code line ends the
// header. Unreadable or uncommented content yields "".
func FileDescription(content []byte) string {
	var parts []string
	inBlock := "" // "*/" or "-->" while inside a block comment
	for i, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if inBlock != "" {
			text := line
			closed := false
			if idx := strings.Index(text, inBlock); idx >= 0 {
				text = text[:idx]
				closed = true
			}
			if t := cleanCommentText(text); t != "" {
				parts = append(parts, t)
			}
			if closed {
				// A block comment is the whole header: what follows
				// (`# Title` in a Markdown file) is content, not comment.
				break
			}
			continue
		}
		if line == "" {
			if len(parts) > 0 {
				break
			}
			continue
		}
		if i == 0 && strings.HasPrefix(line, "#!") {
			continue
		}
		var text string
		switch {
		case strings.HasPrefix(line, "/*"), strings.HasPrefix(line, "<!--"):
			open, closer := "/*", "*/"
			if strings.HasPrefix(line, "<!--") {
				open, closer = "<!--", "-->"
			}
			text = strings.TrimPrefix(line, open)
			if idx := strings.Index(text, closer); idx >= 0 {
				if t := cleanCommentText(text[:idx]); t != "" {
					parts = append(parts, t)
				}
				return truncateRunes(strings.Join(parts, " "), fileDescriptionMaxChars)
			}
			inBlock = closer
		case strings.HasPrefix(line, "//"):
			text = strings.TrimLeft(line, "/")
		case strings.HasPrefix(line, "--"):
			text = strings.TrimLeft(line, "-")
		case strings.HasPrefix(line, "#"):
			text = strings.TrimLeft(line, "#")
		default:
			// The first code line ends the header, whether or not one was
			// collected: a comment further down is not what the file is for.
			return truncateRunes(strings.Join(parts, " "), fileDescriptionMaxChars)
		}
		if t := cleanCommentText(text); t != "" {
			parts = append(parts, t)
		}
	}
	return truncateRunes(strings.Join(parts, " "), fileDescriptionMaxChars)
}

// cleanCommentText strips the decoration a comment line carries — leading
// `*` on block-comment continuation lines, rulers made of `=`, `-`, `*`, `#`
// — and collapses inner whitespace.
func cleanCommentText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "*")
	s = strings.TrimSpace(s)
	if strings.Trim(s, "=-*#~_") == "" {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
