package main

// `-f json` and `-f yaml` must name the same field the same way.
//
// gopkg.in/yaml.v3 does NOT fall back to a `json:` tag. With no `yaml:` tag it
// lowercases the raw Go field name, so `ConfigFile string \`json:"config_file"\``
// is `config_file` under json and `configfile` under yaml, and
// `Timestamp string \`json:"ts"\`` is `ts` under json and `timestamp` under
// yaml. Both formats are advertised by the same persistent `--format` flag and
// documented as the same data, so a script that switches formats gets a
// document whose keys it cannot find. That is #1211, which cmd_audit.go
// already carries a comment about; this file is the executable form of it.
//
// SCOPE. The rule is not enforced package-wide: `cmd/crewship` holds ~1250
// json-tagged fields whose yaml key would differ, most of them on request
// bodies and server-response decode targets that are never handed to a
// formatter, and a blanket AST guard would be ~1250 findings of which the
// large majority are noise. What is enforced here is every *result* struct
// rendered by the machine formatters — the types that reach stdout. The
// remaining 1164 fields across 160 files are #2119, the #1211 remainder.
//
// Adding a new machine-rendered result struct means adding it to
// yamlParityTypes. That is deliberate: the list is the inventory of what the
// CLI promises as a machine contract, and it is short enough to read.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// yamlParityTypes are the machine-rendered result structs whose json and yaml
// keys must agree. Zero values only — the check is over types, so an omitempty
// field is covered even though a populated document would drop it.
func yamlParityTypes() []any {
	return []any{
		// cmd_config.go
		configShowResult{},
		configCheck{},
		configValidateResult{},
		// cmd_server.go
		serverCurrentResult{},
		// cmd_telemetry.go
		telemetryStatusResult{},
		// cmd_lint.go
		lintFinding{},
		lintResult{},
		// cmd_crew.go
		crewStatusAgent{},
		crewStatusAssignment{},
		crewStatusEscalation{},
		crewStatusResult{},
		// cmd_crew_config.go
		crewConfigShowResult{},
		// cmd_admin.go
		adminSessionRow{},
		adminSessionsResult{},
		// cmd_db_migration_status.go
		migrationRef{},
		migrationStatusResult{},
		// cmd_memory.go
		memoryScopeStatus{},
		scopedResult{},
		// cmd_logs.go
		logEntry{},
		// cmd_routine_extra.go
		activeRunRow{},
		// cmd_routine_pending.go
		pendingTriggerRow{},
		// cmd_routine_metadata.go
		runTreeNode{},
		// cmd_routine_overrides.go
		stepOverrideRow{},
		// cmd_routine_replay.go
		errorGroupRow{},
		// cmd_pipeline.go
		dryRunStep{},
		dryRunResult{},
		pipelineRowJSON{},
		// cmd_persona.go
		personaHistoryEntry{},
		personaView{},
		PersonaResponse{},
		// cmd_digest.go
		digestEnableResult{},
		// Row types this change newly renders as YAML. They predate it, but
		// the commands that emit them are among the 44 it fixed, so `-f yaml`
		// on them went from "prints human text" to "prints a document with
		// the wrong keys" — which is this PR's defect, not #2119's backlog.
		// cmd_credential_field.go
		credFieldRow{},
		// cmd_keeper_history.go
		keeperRequestEvent{},
		// cmd_routine_extra.go
		pipelineVersionRow{},
		versionDiffRow{},
		// cmd_routine_schedules.go
		scheduleRow{},
		// cmd_routine_waitpoints.go
		waitpointRow{},
		// cmd_routine_webhooks.go
		WebhookRow{},
		webhookCreateResult{},
		webhookURLResult{},
		// Payloads this change turned from "panics under -f yaml" into
		// "renders under -f yaml". Exporting the embedded type fixed the
		// crash; that is what put their KEYS on the machine contract, so
		// they belong here rather than in #2119 — the guard has to be able
		// to see every type the sweep newly reaches, or the next --format
		// widening reintroduces the same drift silently.
		// cmd_activity.go
		ActivityRow{},
		activityExport{},
		// cmd_model_price.go
		PriceExplain{},
		rateCard{},
		priceChannel{},
		modelPriceResult{},
		// cmd_notifychannel.go
		NotifyChannelRow{},
		notifyChannelCreateResult{},
		// cmd_provider_check.go
		CheckTarget{},
		providerCheckResult{},
		// internal/cli/errors.go — the failure-side counterpart of every
		// struct above. Not a cmd/crewship type, but it is emitted by every
		// command in every machine format, which makes it the one this list
		// could least afford to omit.
		cli.ErrorEnvelope{},
	}
}

func TestMachineResultStructsHaveMatchingJSONAndYAMLKeys(t *testing.T) {
	t.Parallel()

	types := yamlParityTypes()
	if len(types) == 0 {
		t.Fatal("yamlParityTypes is empty — the guard covers nothing")
	}
	for _, v := range types {
		rt := reflect.TypeOf(v)
		t.Run(rt.Name(), func(t *testing.T) {
			checkYAMLKeyParity(t, rt, rt.Name(), map[reflect.Type]bool{})
		})
	}

	// Anti-vacuity, using the count the walker returns for exactly this
	// purpose: if the tag lookup or the struct walk ever stops finding fields,
	// every subtest above passes while checking nothing. One field per listed
	// type is a floor no real result struct falls below.
	//
	// Counted in a separate pass against a throwaway *testing.T rather than
	// summed from the subtests: `go test -run …/ErrorEnvelope` skips the rest,
	// which would leave the total short and fail the parent for no reason.
	// Findings are reported by the subtests above; this pass only counts.
	probe := &testing.T{}
	checked := 0
	for _, v := range types {
		rt := reflect.TypeOf(v)
		checked += checkYAMLKeyParity(probe, rt, rt.Name(), map[reflect.Type]bool{})
	}
	if checked < len(types) {
		t.Errorf("walked %d tagged field(s) across %d result types — the walker has gone blind",
			checked, len(types))
	}
}

// checkYAMLKeyParity walks rt and every struct reachable from it, reporting
// each field whose yaml key would differ from its json key. Returns the number
// of fields inspected so the caller can prove the walker is not blind.
func checkYAMLKeyParity(t *testing.T, rt reflect.Type, path string, seen map[reflect.Type]bool) int {
	t.Helper()
	rt = derefType(rt)
	if rt.Kind() != reflect.Struct || seen[rt] {
		return 0
	}
	seen[rt] = true

	n := 0
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported
		}
		jsonTag, hasJSON := f.Tag.Lookup("json")
		if !hasJSON {
			continue
		}
		n++
		yamlTag, hasYAML := f.Tag.Lookup("yaml")
		where := path + "." + f.Name

		jsonName, jsonOpts := splitTag(jsonTag)
		yamlName, yamlOpts := splitTag(yamlTag)

		switch {
		case jsonTag == "-":
			// encoding/json drops the field; yaml.v3 would still emit it under
			// the lowercased Go name. On a credential-bearing struct that is a
			// leak, not a casing nit.
			if yamlTag != "-" {
				t.Errorf("%s: `json:\"-\"` but no `yaml:\"-\"` — the field is omitted from "+
					"-f json and still emitted by -f yaml", where)
			}
		case f.Anonymous && jsonName == "":
			// An embedded struct: encoding/json flattens it, yaml.v3 nests it
			// under the type name unless told to inline.
			if !hasYAML || !strings.Contains(yamlOpts, "inline") {
				t.Errorf("%s: embedded field is flattened into -f json but nested by -f yaml; "+
					"add `yaml:\",inline\"`", where)
			}
		case !hasYAML:
			effective := strings.ToLower(f.Name)
			want := jsonName
			if want == "" {
				want = f.Name
			}
			if effective != want {
				t.Errorf("%s: -f json key %q, -f yaml key %q (yaml.v3 lowercases the Go field "+
					"name; it does not read json tags) — add `yaml:%q` (#1211)",
					where, want, effective, jsonTag)
			}
		default:
			want := jsonName
			if want == "" {
				want = f.Name
			}
			got := yamlName
			if got == "" {
				got = strings.ToLower(f.Name)
			}
			if got != want {
				t.Errorf("%s: -f json key %q but -f yaml key %q", where, want, got)
			}
			if strings.Contains(jsonOpts, "omitempty") != strings.Contains(yamlOpts, "omitempty") {
				t.Errorf("%s: omitempty is set for one format and not the other "+
					"(json=%q yaml=%q) — the two documents differ on an empty value",
					where, jsonTag, yamlTag)
			}
		}

		n += checkYAMLKeyParity(t, elemType(f.Type), where, seen)
	}
	return n
}

func derefType(rt reflect.Type) reflect.Type {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	return rt
}

// elemType unwraps pointers, slices, arrays and map values so a `[]rowStruct`
// or `map[string]rowStruct` field is walked too.
func elemType(rt reflect.Type) reflect.Type {
	for {
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			rt = rt.Elem()
		case reflect.Map:
			rt = rt.Elem()
		default:
			return rt
		}
	}
}

func splitTag(tag string) (name, opts string) {
	name, opts, _ = strings.Cut(tag, ",")
	return name, opts
}

// Anti-vacuity: the walker above is worthless if it stops seeing fields, and a
// typo in a tag lookup would make every subtest pass silently. Drive it with a
// struct that is deliberately wrong and assert it complains.
func TestYAMLKeyParityWalkerCatchesAMismatch(t *testing.T) {
	t.Parallel()

	type inner struct {
		DeepField string `json:"deep_field"`
	}
	type bad struct {
		ConfigFile string  `json:"config_file"`
		Timestamp  string  `json:"ts"`
		Secret     string  `json:"-"`
		Rows       []inner `json:"rows" yaml:"rows"`
		Fine       string  `json:"fine" yaml:"fine"`
	}

	fake := &testing.T{}
	checkYAMLKeyParity(fake, reflect.TypeOf(bad{}), "bad", map[reflect.Type]bool{})
	if !fake.Failed() {
		t.Fatal("walker passed a struct with four known json/yaml key mismatches")
	}
}
