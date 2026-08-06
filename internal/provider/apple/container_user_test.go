package apple

// ContainerUser must report the container's REAL run-as user.
//
// It used to `return agentContainerUser, nil` for every container id it was
// handed — the constant the create path passes as --user. That is true for a
// crew container this provider created and a fabrication for anything else: a
// crew built from a custom base image whose USER differs, or a container
// running as root.
//
// The fabrication is not cosmetic, because of who asks. resolveExecUser
// (apple_exec.go) calls ContainerUser whenever ExecConfig.User is empty and
// REFUSES the exec when the answer is empty or privileged — the #1158
// fail-closed guard. A constant can never be empty and never privileged, so
// the guard's most important branch could not fire: the one case it exists for
// ("this container has no safe user of its own, do not exec") was unreachable.
// keeper's /execute path (#1060) reads the same method and fails closed on the
// same two conditions, so it was being told the same story.
//
// The payloads below are VERBATIM `container inspect` output captured from
// real containers on container CLI 1.2.0 — see testdata/. This package has
// already shipped two structs written against an imagined payload (`status`
// declared a string when it is an object; an image list with no top-level
// "reference"), each of which failed as a plain absence rather than an error,
// so nothing here is written from memory.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The three fixtures, and what each one proves.
const (
	// A live crew container created by this provider with --user 1001:1001.
	// Requirement: the answer for these must STAY 1001:1001 — but observed,
	// not assumed.
	fixtureCrewContainer = "inspect_crew_container.json"

	// A container from an image carrying `USER 1500:1500` and no --user.
	// The runtime resolves the image's USER at create time, so the payload
	// says 1500:1500 — the exact case the constant lied about. Verified from
	// inside the container: `id` -> uid=1500(probeuser) gid=1500(probeuser).
	fixtureCustomImageUser = "inspect_custom_image_user.json"

	// alpine with no USER directive and no --user: the runtime records the
	// other arm of the union, {"id":{"uid":0,"gid":0}} — root. This is
	// "root by omission", precisely what resolveExecUser must refuse.
	fixtureImageDefaultRoot = "inspect_image_default_root.json"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

// inspectBody is an `inspect` branch for a fake `container` binary, answering
// with the shape a real crew container reports (raw.userString) for whatever id
// it was asked about.
//
// Every fake that exercises an exec with an empty ExecConfig.User needs one
// now: the user is read from the runtime rather than assumed, so a stub that
// answers nothing to `inspect` gets — correctly — a refusal instead of an exec.
// A stub that has to serve the question is the point, not an inconvenience;
// it is what stops these tests passing for a provider that fabricates.
func inspectBody(user string) string {
	return `
case "$1" in
  inspect)
    printf '[{"status":{"state":"running"},"configuration":{"id":"%s","initProcess":{"user":{"raw":{"userString":"%s"}}}}}]' "$2" '` + user + `'
    exit 0;;
esac
`
}

// installFakeContainerServing installs a fake `container` binary that answers
// `inspect` with the given payload and succeeds silently for everything else,
// so the same stub can serve a resolve and then record the exec argv.
func installFakeContainerServing(t *testing.T, inspectPayload string) *fakeCLI {
	t.Helper()
	dir := t.TempDir()
	payloadFile := filepath.Join(dir, "inspect.json")
	if err := os.WriteFile(payloadFile, []byte(inspectPayload), 0o600); err != nil {
		t.Fatalf("write inspect payload: %v", err)
	}
	return installFakeContainer(t, `
case "$1" in
  inspect) cat '`+payloadFile+`'; exit 0;;
esac
exit 0
`)
}

// The union `configuration.initProcess.user` is a Swift enum encoded with the
// case name as the key, so it arrives as one of two disjoint objects. A struct
// that models only one arm silently reads the other as absent — the same
// failure mode as #1779, and here "absent" would mean "undeterminable", which
// turns a perfectly readable root container into a refusal for the wrong
// reason (and, before this change, into a fabricated 1001:1001).
func TestContainerJSON_DecodesEveryRealUserShape(t *testing.T) {
	tests := []struct {
		fixture string
		want    string
		why     string
	}{
		{
			fixture: fixtureCrewContainer,
			want:    agentContainerUser,
			why:     "a crew container created with --user 1001:1001 reports it back verbatim under raw.userString",
		},
		{
			fixture: fixtureCustomImageUser,
			want:    "1500:1500",
			why:     "the runtime resolves the image's USER directive into raw.userString; the old constant claimed 1001:1001 here",
		},
		{
			fixture: fixtureImageDefaultRoot,
			want:    "0:0",
			why:     "no USER and no --user records the id arm as uid 0/gid 0 — root, which the exec guard must be able to see",
		},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			var infos []containerJSON
			if err := json.Unmarshal([]byte(readFixture(t, tt.fixture)), &infos); err != nil {
				t.Fatalf("the real CLI payload must decode: %v", err)
			}
			if len(infos) != 1 {
				t.Fatalf("expected one entry, got %d", len(infos))
			}
			if got := infos[0].ConfiguredUser(); got != tt.want {
				t.Errorf("ConfiguredUser() = %q, want %q — %s", got, tt.want, tt.why)
			}
		})
	}
}

// Neither arm present at all: the honest answer is "I don't know", and the
// contract shared with the docker provider spells that as an empty string with
// a nil error (docker.go:1558 — "An empty string means the image default /
// root, which keeper treats as undeterminable"). Both consumers —
// resolveExecUser and keeper's /execute — refuse on empty, so an empty answer
// fails closed while a guess would not.
func TestContainerJSON_NoUserRecordedIsEmptyNotAGuess(t *testing.T) {
	var infos []containerJSON
	if err := json.Unmarshal([]byte(`[{"configuration":{"id":"x","initProcess":{}}}]`), &infos); err != nil {
		t.Fatalf("a payload without a user must still decode: %v", err)
	}
	if got := infos[0].ConfiguredUser(); got != "" {
		t.Errorf("ConfiguredUser() = %q, want \"\" — an unreadable user must not be answered with a constant", got)
	}
}

// The headline: the answer has to come from the container that was asked
// about. A custom base image's user is the case the constant got wrong, and
// getting it wrong means keeper runs a credential-injected command as a uid
// that does not own the agent's files.
func TestContainerUser_ReportsTheContainersRealUser(t *testing.T) {
	fake := installFakeContainerServing(t, readFixture(t, fixtureCustomImageUser))
	p := newTestProvider(Config{})

	got, err := p.ContainerUser(context.Background(), "custom-image-crew")
	if err != nil {
		t.Fatalf("ContainerUser: %v", err)
	}
	if got != "1500:1500" {
		t.Errorf("ContainerUser = %q, want 1500:1500 (the image's USER, read from the container)", got)
	}
	if !fake.hasCall(t, "inspect custom-image-crew") {
		t.Errorf("ContainerUser answered without inspecting the container: %v", fake.calls(t))
	}
}

// Requirement: preserve the behaviour that was genuinely correct. Containers
// this provider creates are created with --user 1001:1001
// (apple_create_args.go), so they must still answer 1001:1001 — the change is
// that the answer is now read back rather than asserted.
func TestContainerUser_CrewContainerStillAnswersTheAgentUser(t *testing.T) {
	installFakeContainerServing(t, readFixture(t, fixtureCrewContainer))
	p := newTestProvider(Config{})

	got, err := p.ContainerUser(context.Background(), "crewship-1-team-quality-cmsg6n9zj000767b5558f")
	if err != nil {
		t.Fatalf("ContainerUser: %v", err)
	}
	if got != agentContainerUser {
		t.Errorf("ContainerUser = %q, want %q — a crew container's user must not change", got, agentContainerUser)
	}
}

// An inspect that fails tells us nothing about the user, so it must surface as
// an error rather than as a value. resolveExecUser turns that into a refusal.
func TestContainerUser_InspectFailureIsAnError(t *testing.T) {
	installFakeContainer(t, `echo 'no such container' >&2; exit 1`)
	p := newTestProvider(Config{})

	got, err := p.ContainerUser(context.Background(), "gone")
	if err == nil {
		t.Fatalf("ContainerUser on a missing container returned %q with no error", got)
	}
	if got != "" {
		t.Errorf("ContainerUser = %q alongside an error, want \"\"", got)
	}
}

// The guard's most important branch, end to end: a container whose real user
// is root must not be exec'd into just because the caller left User empty.
// With the constant this was unreachable — the resolve always returned a safe
// non-root string no matter what the container was.
func TestExec_EmptyUser_RefusesWhenTheContainerRunsAsRoot(t *testing.T) {
	fake := installFakeContainerServing(t, readFixture(t, fixtureImageDefaultRoot))
	p := newTestProvider(Config{})

	_, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "root-container",
		Cmd:         []string{"id"},
		// User intentionally empty — the resolve branch.
	})
	if err == nil {
		t.Fatal("Exec into a root container with an empty user succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "no safe non-root user") {
		t.Errorf("err = %v, want the fail-closed refusal", err)
	}
	if fake.hasCall(t, "exec") {
		t.Errorf("refused exec still ran the command: %v", fake.calls(t))
	}
}

// Same guard, the undeterminable branch: a payload that records no user must
// refuse too. allowPrivileged deliberately does not cover this — resolveExecUser
// refuses regardless — because "root by omission" is the accident being
// prevented.
func TestExec_EmptyUser_RefusesWhenTheUserCannotBeDetermined(t *testing.T) {
	fake := installFakeContainerServing(t, `[{"configuration":{"id":"mystery"}}]`)
	p := newTestProvider(Config{})

	_, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID:     "mystery",
		Cmd:             []string{"id"},
		AllowPrivileged: true, // must NOT rescue the resolve branch
	})
	if err == nil {
		t.Fatal("Exec with an undeterminable user succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "no safe non-root user") {
		t.Errorf("err = %v, want the fail-closed refusal", err)
	}
	if fake.hasCall(t, "exec") {
		t.Errorf("refused exec still ran the command: %v", fake.calls(t))
	}
}

// And an inspect failure must refuse rather than fall back to anything.
func TestExec_EmptyUser_RefusesWhenInspectFails(t *testing.T) {
	fake := installFakeContainer(t, `exit 1`)
	p := newTestProvider(Config{})

	_, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "gone",
		Cmd:         []string{"id"},
	})
	if err == nil {
		t.Fatal("Exec succeeded although the run-as user could not be resolved; want refusal")
	}
	if !strings.Contains(err.Error(), "resolve run-as user") {
		t.Errorf("err = %v, want a resolve failure", err)
	}
	if fake.hasCall(t, "exec") {
		t.Errorf("refused exec still ran the command: %v", fake.calls(t))
	}
}

// The interactive path (the web terminal) shares resolveExecUser, so it must
// refuse a root container on the same evidence.
func TestExecInteractive_EmptyUser_RefusesWhenTheContainerRunsAsRoot(t *testing.T) {
	installFakeContainerServing(t, readFixture(t, fixtureImageDefaultRoot))
	p := newTestProvider(Config{})

	_, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "root-container",
		Cmd:         []string{"sh"},
	})
	if err == nil {
		t.Fatal("interactive exec into a root container succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "no safe non-root user") {
		t.Errorf("err = %v, want the fail-closed refusal", err)
	}
}
