package craftapps

import "time"

// State is the actual operating state an administrator sees.
type State string

const (
	StateReady       State = "ready"
	StateDraining    State = "draining"
	StateDisabled    State = "disabled"
	StateUnavailable State = "unavailable"
)

// Reason explains a state that is not ready. Every reason has a test that
// produces it, and Admin, the CLI and `crewship doctor` show the same code.
type Reason string

const (
	ReasonNone                   Reason = ""
	ReasonAppDisabled            Reason = "app_disabled"
	ReasonAppDraining            Reason = "app_draining"
	ReasonComponentMissing       Reason = "component_missing"
	ReasonComponentIntegrity     Reason = "component_integrity"
	ReasonNotInstalled           Reason = "component_not_installed"
	ReasonInstalling             Reason = "component_installing"
	ReasonInstallFailed          Reason = "component_install_failed"
	ReasonPlatformUnsupported    Reason = "platform_unsupported"
	ReasonClockUnsupported       Reason = "clock_unsupported"
	ReasonEngineStartFailed      Reason = "engine_start_failed"
	ReasonEngineSelftestFailed   Reason = "engine_selftest_failed"
	ReasonEditorIsolationFailed  Reason = "editor_isolation_failed"
	ReasonEditorIsolationPending Reason = "editor_isolation_pending"
	ReasonAgentsNeedApplication  Reason = "agents_need_application"
)

// Message is the one sentence Admin shows next to a reason.
func (r Reason) Message() string {
	switch r {
	case ReasonAppDisabled:
		return "Turned off. Existing documents stay readable; nobody can create or edit them."
	case ReasonAppDraining:
		return "Turning off. Open editors may finish saving; nobody can start new work."
	case ReasonComponentMissing:
		return "This Crewship build trusts no package of the application. Install an official release."
	case ReasonComponentIntegrity:
		return "The installed application does not match the package this release trusts. Install it again."
	case ReasonNotInstalled:
		return "Not installed. Turning it on downloads and verifies it; offline, install it from a file."
	case ReasonInstalling:
		return "Downloading and verifying the application."
	case ReasonInstallFailed:
		return "The download or its verification failed. Try again, or install it from a file."
	case ReasonPlatformUnsupported:
		return "The application does not run on this operating system or processor yet."
	case ReasonClockUnsupported:
		return "This system has no suspend-aware clock for edit locks."
	case ReasonEngineStartFailed:
		return "The document engine could not start. See the server log."
	case ReasonEngineSelftestFailed:
		return "The document engine failed its self-test. See the server log."
	case ReasonEditorIsolationFailed:
		return "A proxy in front of Crewship rewrites or redirects /_editor/, so the editor is blocked."
	case ReasonEditorIsolationPending:
		return "The editor's proxy check runs once a signed-in browser has used Crewship."
	case ReasonAgentsNeedApplication:
		return "Turn on Word or Sheet first."
	}
	return ""
}

// Component describes the application's runnable parts in this build.
type Component struct {
	Available bool   `json:"available"`
	Reason    Reason `json:"reason,omitempty"`
	// Development is true when the parts come from operator configuration
	// rather than the release itself.
	Development    bool   `json:"development"`
	Version        string `json:"version,omitempty"`
	EngineRevision string `json:"engine_revision,omitempty"`
	EngineSHA256   string `json:"engine_sha256,omitempty"`
	EditorBundle   string `json:"editor_bundle,omitempty"`
	Runtime        string `json:"runtime,omitempty"` // "wasm" or "native"
	// Detail is the last installation error, for the administrator.
	Detail string `json:"detail,omitempty"`
}

// Evaluate combines the desired setting with what can actually run. An
// application whose components cannot run is unavailable whatever its desired
// state, so Admin never shows a green "on" over something that cannot work.
func Evaluate(s Setting, c Component, now time.Time) (State, Reason) {
	if !c.Available {
		r := c.Reason
		if r == ReasonNone {
			r = ReasonComponentMissing
		}
		return StateUnavailable, r
	}
	switch s.Mode(now) {
	case ModeOpen:
		return StateReady, ReasonNone
	case ModeDraining:
		return StateDraining, ReasonAppDraining
	}
	return StateDisabled, ReasonAppDisabled
}
