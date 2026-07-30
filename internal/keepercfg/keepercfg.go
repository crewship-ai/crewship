// Package keepercfg is the runtime-tunable instance configuration for the
// Keeper credential-access judge.
//
// Before this package, `keeper.enabled`, `keeper.ollama_url` and `keeper.model`
// were boot-time only: cfg.Keeper, populated from KEEPER_* env or YAML and
// captured by value while the server was being constructed. The admin console
// could therefore DIAGNOSE a dead judge — "Not running — disabled by
// configuration (keeper.enabled = false)" — but not fix it, and an operator
// without shell access to the box could not turn Keeper on at all. For a
// self-hosted product whose pitch is "runs fully local, no API key", the local
// case was the one that could not be configured.
//
// The layering, per field, lowest to highest precedence:
//
//	built-in default  ←  cfg.Keeper (env/YAML)  ←  keeper_runtime_settings
//	    SourceDefault        SourceEnv                  SourceInstance
//
// Resolution returns the value AND its provenance, so every control can render
// "inherited from server config" versus "instance override" with a working
// Reset. Clearing an override returns the field to the env value rather than to
// a hardcoded guess, which is what makes the two levels tolerable rather than
// confusing.
//
// A per-workspace governance model (keeper_governance_settings, M2a #1001)
// still overrides all of this at request time; what lives here is the instance
// default that overrides.
//
// Scope note: the endpoint stored here is JUDGE-SCOPED. cfg.Keeper.OllamaURL
// also builds the episodic embedder and the chat summarizer (internal/server),
// and moving the embedder silently invalidates every stored vector — so this
// value deliberately repoints the judge only.
package keepercfg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// Judge provider identifiers. Aliased to the governance vocabulary rather than
// redeclared: the instance default and the per-workspace override name the same
// providers, and two spellings of "ollama" is a bug waiting to happen.
const (
	ProviderOllama       = governance.ProviderOllama
	ProviderOpenAICompat = governance.ProviderOpenAICompat
	ProviderAnthropic    = governance.ProviderAnthropic
)

// Wire formats — which HTTP shape the judge speaks to its endpoint. This is the
// distinction that makes a stored URL unambiguous: the same
// `http://host:11434` is `{root}/api/chat` for native Ollama and
// `{root}/v1/chat/completions` for an OpenAI-compatible server, and guessing
// wrong is a 404, which for a fail-closed judge is a DENY on every credential
// request.
//
// NOTE: internal/llm/endpoint (#1528) declares the same four wires as its
// canonical vocabulary. Whichever of the two lands second should make this
// delegate to endpoint.KnownWire and drop the duplicate list — the strings are
// deliberately identical so that swap is mechanical.
const (
	WireOllama            = "ollama"
	WireOpenAIChat        = "openai-chat"
	WireOpenAIResponses   = "openai-responses"
	WireAnthropicMessages = "anthropic-messages"
)

// KnownWire reports whether w is a supported wire format.
func KnownWire(w string) bool {
	switch w {
	case WireOllama, WireOpenAIChat, WireOpenAIResponses, WireAnthropicMessages:
		return true
	default:
		return false
	}
}

// Bounds. maxEndpointLen matches internal/llm/endpoint's raw-input ceiling;
// maxModelLen matches governance.MaxGovModelIDLen so an instance default and a
// workspace override cannot disagree about what fits.
const (
	maxEndpointLen = 2048
	maxModelLen    = governance.MaxGovModelIDLen
)

// singletonID is the only legal primary key — the table is one row by design
// (the CHECK constraint in the migration enforces it), because this is instance
// configuration, not a collection.
const singletonID = "singleton"

// Source is where an effective value came from.
type Source string

const (
	// SourceDefault: nothing configured anywhere; this is the built-in.
	SourceDefault Source = "default"
	// SourceEnv: cfg.Keeper, i.e. KEEPER_* env or YAML at boot.
	SourceEnv Source = "env"
	// SourceInstance: the keeper_runtime_settings row.
	SourceInstance Source = "instance"
)

// Defaults is the boot-time judge config this store layers over — cfg.Keeper,
// passed as a plain struct so this package does not depend on internal/config
// (and so tests can state a scenario in one literal).
type Defaults struct {
	Enabled     bool
	EndpointURL string
	Model       string
}

// TriBool is the wire form of the `enabled` override. Three states, not two:
// "the operator has not touched this" must stay distinguishable from "the
// operator turned it off", or honouring KEEPER_ENABLED becomes impossible to
// tell apart from silently overriding it.
type TriBool string

const (
	TriInherit TriBool = "inherit"
	TriOn      TriBool = "on"
	TriOff     TriBool = "off"
)

// ParseTriBool maps operator input ("on"/"true"/"1", "off"/"false"/"0",
// "inherit"/"default"/"") onto a TriBool. ok is false for anything else.
func ParseTriBool(s string) (TriBool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "enable", "enabled":
		return TriOn, true
	case "off", "false", "0", "no", "disable", "disabled":
		return TriOff, true
	case "inherit", "default", "unset", "":
		return TriInherit, true
	default:
		return "", false
	}
}

// Field is one resolved setting: its effective value plus where that value came
// from, so a caller never has to re-derive provenance by comparing against the
// env config itself.
type Field[T any] struct {
	Value  T      `json:"value"`
	Source Source `json:"source"`
}

// Effective is the fully resolved instance judge configuration.
type Effective struct {
	Enabled     Field[bool]   `json:"enabled"`
	Provider    Field[string] `json:"judge_provider"`
	EndpointURL Field[string] `json:"judge_endpoint_url"`
	Wire        Field[string] `json:"judge_wire"`
	Model       Field[string] `json:"judge_model"`

	// Overridden is true when any field is set at instance level — the one bit
	// a "Reset all" control needs to know whether it would do anything.
	Overridden bool   `json:"overridden"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

// JudgeFingerprint identifies the wiring a judge would be built from. A lazy
// builder caches on this, so a config edit yields a new fingerprint and the
// next evaluation rebuilds — while an edit that changed only who saved it does
// not churn a live provider.
//
// Length-prefixed rather than delimiter-joined: a model tag may contain almost
// anything, and "a|b" and "a" + "|b" must not fingerprint alike.
func (e Effective) JudgeFingerprint() string {
	var b strings.Builder
	for _, part := range []string{
		strconv.FormatBool(e.Enabled.Value),
		e.Provider.Value,
		e.EndpointURL.Value,
		e.Wire.Value,
		e.Model.Value,
	} {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	return b.String()
}

// JudgeConfigured reports whether the effective config could build a judge at
// all. Enabling Keeper without one would make every credential request a DENY
// (the evaluator is fail-closed), so this is checked at configure time rather
// than discovered in production.
func (e Effective) JudgeConfigured() bool {
	return e.EndpointURL.Value != "" && e.Model.Value != ""
}

// settings is the raw stored row. A nil/empty field means "inherit".
type settings struct {
	enabled     *bool
	provider    string
	endpointURL string
	wire        string
	model       string
	updatedAt   string
	updatedBy   string
}

func (s settings) empty() bool {
	return s.enabled == nil && s.provider == "" && s.endpointURL == "" && s.wire == "" && s.model == ""
}

// Patch is a partial update. A nil field is left alone; a non-nil field is
// written, and for the strings a pointer to "" CLEARS the override so the field
// returns to inheriting. That distinction is why these are pointers rather than
// plain strings — "" is a meaningful value here, not an absent one.
type Patch struct {
	Enabled     *TriBool
	Provider    *string
	EndpointURL *string
	Wire        *string
	Model       *string
}

// empty reports whether the patch would change nothing.
func (p Patch) empty() bool {
	return p.Enabled == nil && p.Provider == nil && p.EndpointURL == nil && p.Wire == nil && p.Model == nil
}

// Store is the DB-backed instance override, cached in memory. Reads happen on
// the credential-access path (the lazy judge asks per evaluation) and on every
// status render, so they must not touch the database.
type Store struct {
	db   *sql.DB
	dflt Defaults

	mu       sync.RWMutex
	cur      settings
	onChange []func(Effective)
}

// New builds a Store over db with dflt (cfg.Keeper) as the inherited layer.
// Call Load before serving.
func New(db *sql.DB, dflt Defaults) *Store {
	return &Store{db: db, dflt: dflt}
}

// Load replaces the in-memory cache from keeper_runtime_settings. A missing row
// is the normal state (no override) and not an error.
//
// Call once at boot, before the store is handed to anything that can write:
// like ratelimitcfg, this reads outside the lock and swaps under it, so a Load
// racing a Set could leave the cache one write behind the (correct) row. There
// is deliberately no runtime reload path.
func (s *Store) Load(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	var (
		enabled                          sql.NullInt64
		provider, endpointURL, wire, mdl string
		updatedAt                        string
		updatedBy                        sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT enabled, judge_provider, judge_endpoint_url, judge_wire, judge_model, updated_at, updated_by
		  FROM keeper_runtime_settings WHERE id = ?`, singletonID).
		Scan(&enabled, &provider, &endpointURL, &wire, &mdl, &updatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		s.mu.Lock()
		s.cur = settings{}
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return fmt.Errorf("keepercfg: load settings: %w", err)
	}

	next := settings{
		provider:    provider,
		endpointURL: endpointURL,
		wire:        wire,
		model:       mdl,
		updatedAt:   updatedAt,
		updatedBy:   updatedBy.String,
	}
	if enabled.Valid {
		v := enabled.Int64 != 0
		next.enabled = &v
	}
	s.mu.Lock()
	s.cur = next
	s.mu.Unlock()
	return nil
}

// Defaults returns the inherited (env/YAML) layer.
func (s *Store) Defaults() Defaults {
	if s == nil {
		return Defaults{}
	}
	return s.dflt
}

// Effective resolves every field against the stored row and the env defaults.
// Nil-receiver safe: a process with no store (CLI, unit tests) reports the
// built-in default rather than panicking or inventing an enabled judge.
func (s *Store) Effective() Effective {
	if s == nil {
		// No store means no env layer either — nothing has been configured, so
		// every field reports the built-in rather than an env "off" that no
		// config file actually said.
		return Effective{
			Enabled:     Field[bool]{Source: SourceDefault},
			Provider:    Field[string]{Value: ProviderOllama, Source: SourceDefault},
			EndpointURL: Field[string]{Source: SourceDefault},
			Wire:        Field[string]{Value: WireOllama, Source: SourceDefault},
			Model:       Field[string]{Source: SourceDefault},
		}
	}
	s.mu.RLock()
	cur := s.cur
	s.mu.RUnlock()
	return resolve(cur, s.dflt)
}

// resolve is the pure layering function — the part worth testing exhaustively,
// kept free of the store's locking and SQL.
func resolve(cur settings, dflt Defaults) Effective {
	eff := Effective{
		Overridden: !cur.empty(),
		UpdatedAt:  cur.updatedAt,
		UpdatedBy:  cur.updatedBy,
	}

	// Enabled: cfg.Keeper is the authority whether it says true or false — an
	// unset KEEPER_ENABLED is still the env layer answering "off", so there is
	// no SourceDefault case here.
	if cur.enabled != nil {
		eff.Enabled = Field[bool]{Value: *cur.enabled, Source: SourceInstance}
	} else {
		eff.Enabled = Field[bool]{Value: dflt.Enabled, Source: SourceEnv}
	}

	// Provider and wire have no env equivalent: cfg.Keeper builds a native
	// Ollama judge by construction (llm.NewOllama), so the built-in default
	// names exactly that rather than leaving the field blank.
	eff.Provider = pick(cur.provider, "", ProviderOllama)
	eff.Wire = pick(cur.wire, "", WireOllama)
	eff.EndpointURL = pick(cur.endpointURL, dflt.EndpointURL, "")
	eff.Model = pick(cur.model, dflt.Model, "")
	return eff
}

// pick layers one string field: instance override, else env, else built-in.
func pick(instance, env, builtin string) Field[string] {
	switch {
	case instance != "":
		return Field[string]{Value: instance, Source: SourceInstance}
	case env != "":
		return Field[string]{Value: env, Source: SourceEnv}
	default:
		return Field[string]{Value: builtin, Source: SourceDefault}
	}
}

// Apply validates and persists a partial update, then returns the new effective
// config. Validation runs against the POST-patch state, so an operator can
// enable Keeper and supply its endpoint and model in a single call.
func (s *Store) Apply(ctx context.Context, p Patch, actor string) (Effective, error) {
	if s == nil || s.db == nil {
		return Effective{}, errors.New("keepercfg: no settings store configured")
	}
	if p.empty() {
		return s.Effective(), nil
	}

	s.mu.Lock()
	next := s.cur
	if p.Enabled != nil {
		switch *p.Enabled {
		case TriInherit:
			next.enabled = nil
		case TriOn:
			v := true
			next.enabled = &v
		case TriOff:
			v := false
			next.enabled = &v
		default:
			s.mu.Unlock()
			return Effective{}, newValidation(fmt.Sprintf("enabled must be one of on, off, inherit (got %q)", *p.Enabled))
		}
	}
	if p.Provider != nil {
		next.provider = strings.TrimSpace(*p.Provider)
	}
	if p.EndpointURL != nil {
		next.endpointURL = strings.TrimSpace(*p.EndpointURL)
	}
	if p.Wire != nil {
		next.wire = strings.TrimSpace(*p.Wire)
	}
	if p.Model != nil {
		next.model = strings.TrimSpace(*p.Model)
	}

	if err := validate(next, s.dflt); err != nil {
		s.mu.Unlock()
		return Effective{}, err
	}
	// Persist before touching the cache: a failed write must not leave the
	// process reporting a configuration the database never accepted.
	if err := s.persist(ctx, next, actor); err != nil {
		s.mu.Unlock()
		return Effective{}, err
	}
	s.cur = next
	s.mu.Unlock()

	// Re-read so updated_at/updated_by come from the row the DB actually wrote.
	if err := s.Load(ctx); err != nil {
		return Effective{}, err
	}
	eff := s.Effective()
	s.fireOnChange(eff)
	return eff, nil
}

// Reset drops every instance override so the whole judge config returns to
// cfg.Keeper. Resetting an already-clean instance is a no-op success.
func (s *Store) Reset(ctx context.Context, actor string) (Effective, error) {
	if s == nil || s.db == nil {
		return Effective{}, errors.New("keepercfg: no settings store configured")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM keeper_runtime_settings WHERE id = ?`, singletonID); err != nil {
		return Effective{}, fmt.Errorf("keepercfg: reset settings: %w", err)
	}
	s.mu.Lock()
	s.cur = settings{}
	s.mu.Unlock()

	eff := s.Effective()
	s.fireOnChange(eff)
	return eff, nil
}

// persist writes the singleton row. Timestamps are computed in SQL, never
// formatted in Go, so this stays clear of the RFC3339-near-SQL lint.
func (s *Store) persist(ctx context.Context, next settings, actor string) error {
	var enabled any
	if next.enabled != nil {
		if *next.enabled {
			enabled = 1
		} else {
			enabled = 0
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO keeper_runtime_settings
			(id, enabled, judge_provider, judge_endpoint_url, judge_wire, judge_model, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(id) DO UPDATE SET
			enabled            = excluded.enabled,
			judge_provider     = excluded.judge_provider,
			judge_endpoint_url = excluded.judge_endpoint_url,
			judge_wire         = excluded.judge_wire,
			judge_model        = excluded.judge_model,
			updated_by         = excluded.updated_by,
			updated_at         = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		singletonID, enabled, next.provider, next.endpointURL, next.wire, next.model, nullIfEmpty(actor))
	if err != nil {
		return fmt.Errorf("keepercfg: persist settings: %w", err)
	}
	return nil
}

// OnChange registers a callback fired synchronously after a committed Apply or
// Reset, with the new effective config. The orchestrator's Keeper gate and the
// lazy judge use this — it is what makes `enabled` live instead of boot-time.
// Callbacks must be cheap and must not call back into the store.
func (s *Store) OnChange(fn func(Effective)) {
	if s == nil || fn == nil {
		return
	}
	s.mu.Lock()
	s.onChange = append(s.onChange, fn)
	s.mu.Unlock()
}

func (s *Store) fireOnChange(eff Effective) {
	s.mu.RLock()
	cbs := make([]func(Effective), len(s.onChange))
	copy(cbs, s.onChange)
	s.mu.RUnlock()
	for _, fn := range cbs {
		fn(eff)
	}
}

// --- validation -------------------------------------------------------------

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func newValidation(msg string) error { return &validationError{msg} }

// IsValidation reports whether err is bad operator input rather than an
// infrastructure failure, so the handler can pick 400 over 500.
func IsValidation(err error) bool {
	var v *validationError
	return errors.As(err, &v)
}

// validate checks the post-patch row. Errors are written to be read by the
// person who typed the value, not by us.
func validate(next settings, dflt Defaults) error {
	if next.provider != "" {
		if !governance.KnownGovProvider(next.provider) {
			return newValidation(fmt.Sprintf("unknown judge provider %q — use %s or %s",
				next.provider, ProviderOllama, ProviderOpenAICompat))
		}
		// The instance row carries no credential reference by design: auth
		// belongs in the vault, which is workspace-scoped. So a provider that
		// cannot dial without a key has no way to work here.
		if next.provider == ProviderAnthropic {
			return newValidation("the anthropic judge needs an API key, which is stored per workspace — " +
				"configure it as the workspace governance model instead of an instance default")
		}
		// Everything except native Ollama needs the request URL derived from the
		// stored endpoint, and doing that unambiguously is the endpoint
		// contract's job (internal/llm/endpoint, #1528). Storing the value now
		// would mean a judge that POSTs to a guessed path — the exact
		// "configuration looks fine, every request DENYs" failure this table
		// exists to end. Say so rather than accept it.
		if next.provider != ProviderOllama {
			return newValidation(fmt.Sprintf(
				"judge provider %q is not configurable as an instance default yet — the instance judge speaks native Ollama; "+
					"configure an OpenAI-compatible judge as the workspace governance model", next.provider))
		}
	}
	if next.wire != "" {
		if !KnownWire(next.wire) {
			return newValidation(fmt.Sprintf("unknown judge wire %q — use one of %s, %s, %s, %s",
				next.wire, WireOllama, WireOpenAIChat, WireOpenAIResponses, WireAnthropicMessages))
		}
		if next.wire != WireOllama {
			return newValidation(fmt.Sprintf(
				"judge wire %q is not available as an instance default yet — the instance judge speaks the native Ollama wire (%s)",
				next.wire, WireOllama))
		}
	}
	if err := validateEndpoint(next.endpointURL); err != nil {
		return err
	}
	if err := validateModel(next.model); err != nil {
		return err
	}

	// Fail-closed guard: with Keeper on and no judge, every credential request
	// DENYs and the reason looks like a security verdict rather than a
	// configuration error. Refuse the configuration instead of shipping the
	// outage. Checked against the resolved values so inheriting the endpoint or
	// model from the env still counts as configured.
	eff := resolve(next, dflt)
	if eff.Enabled.Value && !eff.JudgeConfigured() {
		missing := "endpoint URL and model"
		switch {
		case eff.EndpointURL.Value == "" && eff.Model.Value != "":
			missing = "endpoint URL"
		case eff.EndpointURL.Value != "" && eff.Model.Value == "":
			missing = "model"
		}
		return newValidation("Keeper cannot be enabled without a judge " + missing +
			" — it is fail-closed, so an unreachable judge denies every credential request")
	}
	return nil
}

func validateEndpoint(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > maxEndpointLen {
		return newValidation(fmt.Sprintf("judge endpoint URL is too long (%d characters, limit %d)", len(raw), maxEndpointLen))
	}
	// Checked before parsing: `192.168.1.40:11434` — the single likeliest paste
	// — fails url.Parse with "first path segment in URL cannot contain colon",
	// which tells the operator nothing about the missing http://.
	if !strings.Contains(raw, "://") {
		return newValidation("judge endpoint needs an http:// or https:// scheme — try http://host:11434")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return newValidation(fmt.Sprintf("judge endpoint is not a valid URL: %v", err))
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return newValidation(fmt.Sprintf("judge endpoint scheme %q is not supported — use http or https", u.Scheme))
	}
	if u.Host == "" {
		return newValidation("judge endpoint has no host — try http://host:11434")
	}
	if u.User != nil {
		// This row is not a secret store, and the admin GET echoes it back.
		return newValidation("judge endpoint must not embed credentials — store the token as a credential and reference it per workspace")
	}
	return nil
}

func validateModel(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > maxModelLen {
		return newValidation(fmt.Sprintf("judge model is too long (%d characters, limit %d)", len(raw), maxModelLen))
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return newValidation("judge model contains a control character")
		}
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
