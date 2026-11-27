package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/craftsession"
)

// sheetSessions keeps one persistent engine session manager per pinned engine
// executable, created on first use. Documents are keyed by content, so a
// session that holds the last saved version is found again without opening it.
type sheetSessions struct {
	mu       sync.Mutex
	managers map[string]*craftsession.Manager
	adapters map[*craftsession.Manager]*goapi.CraftNativeSheetAgentAdapter
	// dirs hold the engine modules of installed packages; removed on Close.
	dirs []string
}

func (s *sheetSessions) manager(profile goapi.CraftNativeDocumentProfile) *craftsession.Manager {
	if profile.CLISHA256 == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// One manager per engine digest. An operator profile names its module by
	// path; an installed package carries it as verified bytes, written once to
	// a private file for the workers (as the import does).
	key := profile.CLISHA256
	if s.managers == nil {
		s.managers = map[string]*craftsession.Manager{}
	}
	if m := s.managers[key]; m != nil {
		return m
	}
	path := profile.CLIPath
	if path == "" {
		if profile.EngineModule == nil || profile.EngineModule.Load == nil {
			return nil
		}
		module, err := profile.EngineModule.Load()
		if err != nil || len(module) == 0 {
			return nil
		}
		dir, err := os.MkdirTemp("", "crewship-sheet-engine-")
		if err != nil {
			return nil
		}
		path = filepath.Join(dir, "engine.wasm")
		if err = os.WriteFile(path, module, 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return nil
		}
		s.dirs = append(s.dirs, dir)
	}
	m := craftsession.NewManager(craftsession.Config{
		Launch: craftsession.ExecLauncher("gridcraft", path, profile.CLISHA256, 4096),
		// A large workbook holds about 2.3 GiB while open (1.8 GiB more while it
		// serialises) and the server has no cap on resident memory: two sessions
		// at once. A running operation is never evicted; a third Sheet is told
		// the engine is busy.
		MaxSessions: sheetSessionLimit,
		// Formulas stay off until the owner enables them: the policy is the
		// same rule the command validation applies, a second line of defence.
		InputPolicy: craftsession.InputPolicy(goapi.CraftNativeAgentInputPolicy(false)),
		// Only an engine module known to keep saved values exactly (the
		// measured list, by digest) opens the server's own computed documents
		// without recalculating; every other engine recalculates as before.
		OpenSavedValues: craftsession.SavedValuesFor(profile.CLISHA256),
	})
	s.managers[key] = m
	return m
}

// computed is CraftNativeHTTPConfig.SheetComputed: doc was written by this
// profile's engine with every value recalculated (an import).
func (s *sheetSessions) computed(profile goapi.CraftNativeDocumentProfile, doc []byte) {
	if s == nil || profile.Application != "gridcraft" {
		return
	}
	if m := s.manager(profile); m != nil {
		m.Computed(doc)
	}
}

// trusted is CraftNativeHTTPConfig.SheetTrusted: doc is exactly a document
// this profile's session manager recorded as computed by its own engine.
func (s *sheetSessions) trusted(profile goapi.CraftNativeDocumentProfile, doc []byte) bool {
	if s == nil || profile.Application != "gridcraft" {
		return false
	}
	m := s.manager(profile)
	return m != nil && m.IsComputed(doc)
}

func (s *sheetSessions) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.managers {
		m.Close()
	}
	for _, dir := range s.dirs {
		_ = os.RemoveAll(dir)
	}
	s.managers, s.adapters, s.dirs = nil, nil, nil
}

// sheetEngine adapts a craftsession.Manager to the narrow interface the Sheet
// agent commands use.
type sheetEngine struct{ m *craftsession.Manager }

func contentKey(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sheetSessionLimit is how many Sheets the engine keeps open at once.
const sheetSessionLimit = 2

func (e sheetEngine) Open(ctx context.Context, base []byte) (goapi.CraftNativeSheetSession, func(), error) {
	var (
		s       *craftsession.Session
		release func()
	)
	// Every slot held by other Sheets: wait in turn rather than fail at once.
	err := waitForSlot(ctx, sheetSlotWait, 250*time.Millisecond, func(err error) bool { return errors.Is(err, craftsession.ErrBusy) }, func() (err error) {
		s, release, err = e.m.Acquire(ctx, contentKey(base), func() ([]byte, error) { return base, nil })
		return err
	})
	if errors.Is(err, craftsession.ErrBusy) {
		return nil, nil, goapi.ErrCraftNativeSheetBusy
	}
	if err != nil {
		return nil, nil, err
	}
	return sheetSession{s}, release, nil
}

// Retain moves the warm session to the key of the content it now holds.
func (e sheetEngine) Retain(session goapi.CraftNativeSheetSession, snapshot []byte) {
	if s, ok := session.(sheetSession); ok {
		e.m.Rekey(s.Key(), contentKey(snapshot))
	}
}

// Discard closes a session that may hold changes that were never saved.
func (e sheetEngine) Discard(session goapi.CraftNativeSheetSession) {
	if s, ok := session.(sheetSession); ok {
		s.Session.Close()
	}
}

type sheetSession struct{ *craftsession.Session }

// Apply writes the batch with one re-calculation (craftsession.Session.Apply).
func (s sheetSession) Apply(ctx context.Context, ops []goapi.CraftNativeSheetWrite) (int, error) {
	converted := make([]craftsession.WriteOp, len(ops))
	for i, op := range ops {
		converted[i] = craftsession.WriteOp{Cell: op.Cell, Input: op.Input, Range: op.Range, Values: op.Values}
	}
	return s.Session.Apply(ctx, converted)
}

func (s sheetSession) ReadRange(ctx context.Context, rng string, formulas bool) (goapi.CraftNativeSheetValues, error) {
	v, err := s.Session.ReadRangeWith(ctx, rng, craftsession.ReadOptions{Formulas: formulas})
	if err != nil {
		return goapi.CraftNativeSheetValues{}, err
	}
	return goapi.CraftNativeSheetValues{Range: v.Range, Sheet: v.Sheet, Values: v.Values, HeldValues: v.HeldValues}, nil
}

func (s sheetSession) GetCell(ctx context.Context, cell string) (goapi.CraftNativeSheetCell, error) {
	c, err := s.Session.GetCell(ctx, cell)
	if err != nil {
		return goapi.CraftNativeSheetCell{}, err
	}
	return goapi.CraftNativeSheetCell{Cell: c.Cell, Input: c.Input, Formula: c.Formula, Value: c.Value}, nil
}

func (s sheetSession) Sheets(ctx context.Context) ([]goapi.CraftNativeSheetInfo, error) {
	infos, err := s.Session.Sheets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]goapi.CraftNativeSheetInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, goapi.CraftNativeSheetInfo{Name: i.Name, Used: i.Used})
	}
	return out, nil
}

// nativeCraftAgentEngine routes the agent's apply and inspect: every agent
// change of a Sheet (a chat run's and a native Craft task's) goes through a
// persistent session, so a large workbook is opened once and recalculated once
// per batch, not opened and recalculated again for every call. A Word document
// and a Sheet without an engine keep the one-shot adapter and its input
// validation, unchanged. The session adapter checks a version
// once, after the session opened it, that the engine's own serialisation of it
// is the same document (no separate full open just for validation).
func nativeCraftAgentEngine(sessions *sheetSessions) (goapi.CraftNativeAgentApply, goapi.CraftNativeAgentInspect) {
	oneShot := goapi.NewCraftNativeAgentCLIAdapter()
	adapterFor := func(ctx context.Context, p goapi.CraftNativeDocumentProfile) *goapi.CraftNativeSheetAgentAdapter {
		if p.Application != "gridcraft" {
			return nil
		}
		if m := sessions.manager(p); m != nil {
			sessions.mu.Lock()
			defer sessions.mu.Unlock()
			if sessions.adapters == nil {
				sessions.adapters = map[*craftsession.Manager]*goapi.CraftNativeSheetAgentAdapter{}
			}
			if a := sessions.adapters[m]; a != nil {
				return a
			}
			a := goapi.NewCraftNativeSheetAgentAdapter(sheetEngine{m}, goapi.WithCraftNativeSheetOpenCheck(goapi.CraftNativeSheetSnapshotKeepsEverything))
			sessions.adapters[m] = a
			return a
		}
		return nil
	}
	apply := func(ctx context.Context, p goapi.CraftNativeDocumentProfile, raw []byte, commands json.RawMessage) ([]byte, error) {
		if a := adapterFor(ctx, p); a != nil {
			return a.Apply(ctx, p, raw, commands)
		}
		return oneShot.Apply(ctx, p, raw, commands)
	}
	inspect := func(ctx context.Context, p goapi.CraftNativeDocumentProfile, raw []byte, query json.RawMessage) (json.RawMessage, error) {
		if a := adapterFor(ctx, p); a != nil {
			return a.Inspect(ctx, p, raw, query)
		}
		return oneShot.Inspect(ctx, p, raw, query)
	}
	return apply, inspect
}

// closeWithin ends every session. Each engine worker closes its input, waits
// two seconds and is then killed; this bounds the whole shutdown too and
// reports whether it finished in time.
func (s *sheetSessions) closeWithin(limit time.Duration) bool {
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		return false
	}
}
