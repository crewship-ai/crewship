package devcontainer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// An agent's CLI adapter needs a binary in the crew's container image, and
// nothing used to guarantee it was there. Whether `claude` existed depended on
// the operator remembering the claude-code feature or the `claude` mise tool;
// a wizard-built crew with neither answered every chat message with
// "stdbuf: failed to run command 'claude'". The customer who hits that does
// not open a devcontainer.json — they leave.
//
// This file is the one place that knows which binary each adapter execs and
// how to install it. The provisioning job derives the required set from the
// crew's agents, folds the install recipe into the build, and verifies the
// binaries resolve before the image is called ready (verifyRequiredBinaries).

// AdapterCLI describes the command-line tool one CLI adapter execs.
type AdapterCLI struct {
	// Adapter is the agents.cli_adapter value (CLAUDE_CODE, CODEX_CLI, …).
	Adapter string
	// Binary is the executable the adapter runs (adapter_*.go).
	Binary string
	// MiseTool is the tool name in the mise registry that installs Binary,
	// empty when mise has none.
	MiseTool string
	// FeatureIDs are devcontainer feature ids (the last path segment of the
	// feature ref, before the version) that already install Binary; when the
	// crew declares one, no mise tool is added.
	FeatureIDs []string
	// InstallCommand installs Binary when there is no mise tool. Run as the
	// agent user through postCreateCommand.
	InstallCommand string
}

// adapterCLIs is the catalogue, in adapter order. The mise tool names are the
// ones `crewship runtimes list` shows under the "tools" category; the droid
// installer is the one cmd/crewship/seeddata/builtin/crews.yaml runs.
var adapterCLIs = []AdapterCLI{
	{Adapter: "CLAUDE_CODE", Binary: "claude", MiseTool: "claude", FeatureIDs: []string{"claude-code"}},
	{Adapter: "CODEX_CLI", Binary: "codex", MiseTool: "codex"},
	{Adapter: "GEMINI_CLI", Binary: "gemini", MiseTool: "gemini-cli"},
	{Adapter: "OPENCODE", Binary: "opencode", MiseTool: "opencode"},
	{Adapter: "CURSOR_CLI", Binary: "cursor-agent", MiseTool: "cursor-agent"},
	{Adapter: "FACTORY_DROID", Binary: "droid", InstallCommand: "curl -fsSL https://app.factory.ai/cli -o /tmp/droid-install.sh && bash /tmp/droid-install.sh && rm -f /tmp/droid-install.sh"},
}

// AdapterCLIFor returns the CLI an adapter execs. ok is false for an adapter
// this build does not know, which the caller treats as "nothing to install"
// rather than an error: an unknown adapter is rejected earlier, by the agent
// handlers' validCLIAdapters.
func AdapterCLIFor(adapter string) (AdapterCLI, bool) {
	adapter = strings.ToUpper(strings.TrimSpace(adapter))
	for _, a := range adapterCLIs {
		if a.Adapter == adapter {
			return a, true
		}
	}
	return AdapterCLI{}, false
}

// RequiredAdapterCLIs maps a crew's agents' adapters to the distinct CLIs
// they need, in catalogue order. Unknown and empty adapters are skipped.
func RequiredAdapterCLIs(adapters []string) []AdapterCLI {
	want := map[string]bool{}
	for _, a := range adapters {
		want[strings.ToUpper(strings.TrimSpace(a))] = true
	}
	var out []AdapterCLI
	for _, a := range adapterCLIs {
		if want[a.Adapter] {
			out = append(out, a)
		}
	}
	return out
}

// AdapterCLIPlan is what EnsureAdapterCLIs decided for one crew.
type AdapterCLIPlan struct {
	// MiseConfig is the crew's mise config with the adapter tools added,
	// always JSON (a TOML input is re-encoded). Equal to the input when
	// nothing had to be added and the input was already JSON.
	MiseConfig string
	// AddedTools are the mise tools this call added, in catalogue order.
	AddedTools []string
	// AddedCommands are the postCreateCommand lines this call appended to
	// cfg for CLIs mise cannot install.
	AddedCommands []string
	// Binaries are every required binary, in catalogue order — the set
	// verifyRequiredBinaries checks after the build, whether the install
	// came from a feature, a mise tool, this call or the base image.
	Binaries []string
}

// EnsureAdapterCLIs folds the install recipes of the given adapters into a
// crew's build inputs: a mise tool is added for every required CLI that no
// declared feature already provides, an install command for the CLIs mise
// does not carry. cfg is modified in place (postCreateCommand only); the mise
// config is returned rather than mutated because the caller holds it as a
// string column.
//
// Idempotent: a tool or command that is already present is not added twice,
// so re-provisioning a crew produces the same inputs and the same config hash.
func EnsureAdapterCLIs(cfg *Config, miseConfig string, adapters []string) (AdapterCLIPlan, error) {
	plan := AdapterCLIPlan{MiseConfig: miseConfig}
	required := RequiredAdapterCLIs(adapters)
	if len(required) == 0 {
		return plan, nil
	}
	var mise *MiseConfig
	if strings.TrimSpace(miseConfig) != "" {
		parsed, err := ParseMiseConfig(miseConfig)
		if err != nil {
			return plan, fmt.Errorf("adapter clis: mise config: %w", err)
		}
		mise = parsed
	} else {
		mise = &MiseConfig{Tools: map[string]string{}}
	}
	if mise.Tools == nil {
		mise.Tools = map[string]string{}
	}
	existingCommands := map[string]bool{}
	if cfg != nil {
		for _, c := range cfg.NormalizedPostCreateCommands() {
			existingCommands[c] = true
		}
	}
	miseChanged := false
	for _, a := range required {
		plan.Binaries = append(plan.Binaries, a.Binary)
		if cfg != nil && featureProvidesBinary(cfg, a) {
			continue
		}
		switch {
		case a.MiseTool != "":
			if _, has := mise.Tools[a.MiseTool]; has {
				continue
			}
			mise.Tools[a.MiseTool] = "latest"
			plan.AddedTools = append(plan.AddedTools, a.MiseTool)
			miseChanged = true
		case a.InstallCommand != "":
			if cfg == nil || existingCommands[a.InstallCommand] {
				continue
			}
			cfg.PostCreateCommand = append(cfg.NormalizedPostCreateCommands(), a.InstallCommand)
			existingCommands[a.InstallCommand] = true
			plan.AddedCommands = append(plan.AddedCommands, a.InstallCommand)
		}
	}
	if miseChanged || (mise != nil && strings.TrimSpace(miseConfig) != "" && !strings.HasPrefix(strings.TrimLeft(miseConfig, " \t\r\n"), "{")) {
		// Re-encode as JSON so the stored TOML and the derived tools travel
		// in the one form ParseMiseConfig and the UI both read.
		b, err := json.Marshal(mise)
		if err != nil {
			return plan, fmt.Errorf("adapter clis: encode mise config: %w", err)
		}
		plan.MiseConfig = string(b)
	}
	return plan, nil
}

// featureProvidesBinary reports whether one of the crew's declared features is
// known to install the adapter's binary. Matched on the feature id (the ref's
// last path segment without its version), so any registry that publishes
// "…/claude-code:2" counts.
func featureProvidesBinary(cfg *Config, a AdapterCLI) bool {
	if len(a.FeatureIDs) == 0 || cfg == nil {
		return false
	}
	for ref := range cfg.Features {
		id := featureIDFromRef(ref)
		for _, want := range a.FeatureIDs {
			if id == want {
				return true
			}
		}
	}
	return false
}

// featureIDFromRef extracts "claude-code" from
// "ghcr.io/devcontainers-extra/features/claude-code:2" (or "@sha256:…").
func featureIDFromRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	if i := strings.IndexAny(ref, ":@"); i >= 0 {
		ref = ref[:i]
	}
	return ref
}

// SortedBinaries is a small helper for callers that store the verified set.
func SortedBinaries(bins []string) []string {
	out := append([]string(nil), bins...)
	sort.Strings(out)
	return out
}
