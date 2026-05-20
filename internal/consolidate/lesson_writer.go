package consolidate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"gopkg.in/yaml.v3"
)

// LessonKind discriminates lesson polarity. Downstream readers
// (LessonsLearnedCard widget, F4.4 negative-learning evaluator's
// "what's the agent already aware of?" lookup, future skill-promote
// filtering) all branch on this value.
type LessonKind string

const (
	LessonKindPositive LessonKind = "positive" // a working pattern worth repeating
	LessonKindNegative LessonKind = "negative" // an anti-pattern to avoid
	LessonKindNeutral  LessonKind = "neutral"  // factual observation, no polarity
)

// LessonSource records which subsystem produced the entry so the
// curator + UI can attribute / dedup / prune by origin.
type LessonSource string

const (
	LessonSourceManual           LessonSource = "manual"            // operator-entered via UI / CLI
	LessonSourceSkillPromote     LessonSource = "skill_promote"     // consolidator promoted a stable rule
	LessonSourceNegativeLearning LessonSource = "negative_learning" // F4.4 evaluator (PR-C)
	LessonSourceConsolidator     LessonSource = "consolidator"      // periodic memory sweep
	LessonSourceUserFeedback     LessonSource = "user_feedback"     // explicit "remember this" capture
)

// LessonEntry is the wire shape of a single lessons.md row. Persisted
// as YAML; new fields must be omitempty + non-breaking so older readers
// don't trip on additions.
type LessonEntry struct {
	ID          string       `yaml:"id"`
	Kind        LessonKind   `yaml:"kind"`
	CapturedAt  time.Time    `yaml:"captured_at"`
	Source      LessonSource `yaml:"source"`
	Rule        string       `yaml:"rule"`
	ContextNote string       `yaml:"context,omitempty"`
}

// lessonFile is the on-disk root for lessons.md.
type lessonFile struct {
	Entries []LessonEntry `yaml:"entries"`
}

// lessonsFilename is the single per-agent file the writer maintains.
// PR-Z Z.7 deliberately collapses former plans for date-stamped
// learned-YYYY-MM-DD.md / antipatterns-YYYY-MM.md fragments into one
// long-lived, kind-discriminated log. PR-C is the first real consumer.
const lessonsFilename = "lessons.md"

// validLessonKinds is the closed set the writer accepts. Anything
// outside this gets rejected at the boundary so a typo can't quietly
// land an entry that downstream `kind` filters silently exclude.
var validLessonKinds = map[LessonKind]struct{}{
	LessonKindPositive: {},
	LessonKindNegative: {},
	LessonKindNeutral:  {},
}

// validLessonSources is the closed set of producers. Lets the curator
// and analytics queries group / count by source without string
// matching every freeform label downstream code might invent.
var validLessonSources = map[LessonSource]struct{}{
	LessonSourceManual:           {},
	LessonSourceSkillPromote:     {},
	LessonSourceNegativeLearning: {},
	LessonSourceConsolidator:     {},
	LessonSourceUserFeedback:     {},
}

// WriteLesson appends entry to {agentMemoryDir}/lessons.md, creating
// the file (and any parents) if missing. The write is:
//
//   - Idempotent by entry.ID — re-running with the same ID is a no-op
//     even across retries / hook double-fires.
//   - Flock-serialized via memory.FileLock so concurrent calls from
//     different goroutines (e.g. consolidator + F4.4 evaluator
//     racing on the same agent dir) don't interleave YAML fragments.
//   - Validated at the boundary: empty ID, empty rule, or unknown
//     kind / source returns an error without touching disk. The
//     writer cannot land a row that downstream readers will silently
//     skip via filter.
//
// New fields on LessonEntry should ALWAYS be additive + yaml omitempty
// so older readers parsing newer files don't fail unmarshal. The
// closed kind/source sets are the only stability anchors — extending
// them requires a coordinated bump of validLessonKinds /
// validLessonSources AND the consumers that switch on the value.
func WriteLesson(ctx context.Context, agentMemoryDir string, entry LessonEntry) error {
	if strings.TrimSpace(entry.ID) == "" {
		return errors.New("lesson: id is required (idempotency key)")
	}
	if strings.TrimSpace(entry.Rule) == "" {
		return errors.New("lesson: rule is required (empty entries are noise)")
	}
	if _, ok := validLessonKinds[entry.Kind]; !ok {
		return fmt.Errorf("lesson: invalid kind %q (must be positive | negative | neutral)", entry.Kind)
	}
	if _, ok := validLessonSources[entry.Source]; !ok {
		return fmt.Errorf("lesson: invalid source %q (see LessonSource* constants)", entry.Source)
	}
	if entry.CapturedAt.IsZero() {
		entry.CapturedAt = time.Now().UTC()
	} else {
		entry.CapturedAt = entry.CapturedAt.UTC()
	}

	if err := os.MkdirAll(agentMemoryDir, 0o755); err != nil {
		return fmt.Errorf("lesson: mkdir %s: %w", agentMemoryDir, err)
	}
	path := filepath.Join(agentMemoryDir, lessonsFilename)

	lk := memory.NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("lesson: lock %s: %w", path, err)
	}
	defer func() { _ = lk.Unlock() }()

	current, err := loadLessonsLocked(path)
	if err != nil {
		return err
	}
	// Idempotency: drop existing entry with same ID (last write wins
	// on content), then append. Replace-on-same-ID lets a corrected
	// rule body overwrite an earlier draft without leaving the stale
	// version on disk.
	out := lessonFile{Entries: make([]LessonEntry, 0, len(current.Entries)+1)}
	for _, e := range current.Entries {
		if e.ID == entry.ID {
			continue
		}
		out.Entries = append(out.Entries, e)
	}
	out.Entries = append(out.Entries, entry)

	return saveLessonsLocked(path, out)
}

// ReadLessons returns entries from {agentMemoryDir}/lessons.md.
// If kind is the empty string, all entries are returned; otherwise only
// entries matching kind are returned. Missing file returns nil + nil
// (a fresh agent with no lessons is not an error).
func ReadLessons(ctx context.Context, agentMemoryDir string, kind LessonKind) ([]LessonEntry, error) {
	path := filepath.Join(agentMemoryDir, lessonsFilename)
	file, err := loadLessonsLocked(path)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		return file.Entries, nil
	}
	out := make([]LessonEntry, 0, len(file.Entries))
	for _, e := range file.Entries {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out, nil
}

// loadLessonsLocked reads + parses the file. Caller must hold the
// flock (or the file must be otherwise quiesced). Missing file is
// returned as an empty lessonFile (not an error) so first-write paths
// don't have to branch.
func loadLessonsLocked(path string) (lessonFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return lessonFile{}, nil
	}
	if err != nil {
		return lessonFile{}, fmt.Errorf("lesson: read %s: %w", path, err)
	}
	var f lessonFile
	if len(data) == 0 {
		return f, nil
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return lessonFile{}, fmt.Errorf("lesson: parse %s: %w", path, err)
	}
	return f, nil
}

// saveLessonsLocked writes the file atomically (write to temp +
// rename). Caller must hold the flock.
func saveLessonsLocked(path string, f lessonFile) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("lesson: marshal: %w", err)
	}
	// Prepend a human-readable header so an operator opening the file
	// in an editor sees what it is. The body remains valid YAML
	// because `#` starts a YAML comment.
	header := "# Lessons learned by this agent. Written by internal/consolidate/lesson_writer.go (PR-Z Z.7).\n" +
		"# Append-only by ID; safe to commit alongside other agent memory.\n" +
		"# Kind: positive (worth repeating) | negative (avoid) | neutral (factual).\n\n"
	out := append([]byte(header), data...)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("lesson: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("lesson: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}
