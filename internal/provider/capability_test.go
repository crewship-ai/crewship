package provider_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// reportingProvider is a ContainerProvider that also answers the capability
// question. mockContainerProvider (types_test.go) deliberately does NOT, so the
// two together cover both sides of the optional-interface assertion.
type reportingProvider struct {
	mockContainerProvider
	report provider.CrewConfigSupport
}

func (r *reportingProvider) UnsupportedCrewConfig(_ provider.CrewConfig) provider.CrewConfigSupport {
	return r.report
}

// TestInspectCrewConfigSupport_ProviderWithoutReporterClaimsNothing pins the
// default for a provider that has not opted in: an empty report, never a
// fabricated one. A provider that says nothing is read as "I honour what I was
// given" — which is the docker provider's position — so a helper that invented
// warnings here would put false drops on every crew.
func TestInspectCrewConfigSupport_ProviderWithoutReporterClaimsNothing(t *testing.T) {
	got := provider.InspectCrewConfigSupport(&mockContainerProvider{}, provider.CrewConfig{
		ID: "crew-1", Slug: "ops", NetworkMode: "restricted",
	})
	if !got.Empty() {
		t.Fatalf("provider without CrewConfigReporter must report nothing, got %+v", got)
	}
	if got.RefusedError("mock") != nil {
		t.Errorf("empty report must not produce a refusal error")
	}
}

// TestInspectCrewConfigSupport_NilProviderIsSafe covers the callers that hold a
// possibly-nil ContainerProvider (orchestrator/chatbridge both check for nil
// separately, and a panic here would turn a config warning into a crash).
func TestInspectCrewConfigSupport_NilProviderIsSafe(t *testing.T) {
	if !provider.InspectCrewConfigSupport(nil, provider.CrewConfig{ID: "c"}).Empty() {
		t.Fatal("nil provider must report nothing")
	}
}

// TestInspectCrewConfigSupport_ReporterIsConsulted proves the type assertion
// actually reaches the implementation rather than falling through to the
// empty default.
func TestInspectCrewConfigSupport_ReporterIsConsulted(t *testing.T) {
	p := &reportingProvider{report: provider.CrewConfigSupport{
		Degraded: []provider.DroppedField{{Field: "TTLHours", Detail: "no idle auto-stop"}},
	}}
	got := provider.InspectCrewConfigSupport(p, provider.CrewConfig{ID: "crew-1"})
	if got.Empty() {
		t.Fatal("reporter's answer must be returned, got an empty report")
	}
	if len(got.Degraded) != 1 || got.Degraded[0].Field != "TTLHours" {
		t.Fatalf("Degraded = %+v, want the reporter's single TTLHours entry", got.Degraded)
	}
}

// TestCrewConfigSupport_RefusedErrorIsIdentifiableAndNamesEveryField is the
// contract the callers depend on: a refusal must be matchable with errors.Is
// (so chatbridge can classify it without substring-sniffing) AND must name the
// provider plus every refused field in its text (so the operator reading a log
// line knows what to change).
func TestCrewConfigSupport_RefusedErrorIsIdentifiableAndNamesEveryField(t *testing.T) {
	s := provider.CrewConfigSupport{Refused: []provider.DroppedField{
		{Field: "NetworkMode", Value: "restricted", Detail: "no sidecar proxy reaches the container"},
		{Field: "SomethingElse", Detail: "also impossible"},
	}}
	err := s.RefusedError("apple-container")
	if err == nil {
		t.Fatal("a non-empty Refused list must produce an error")
	}
	if !errors.Is(err, provider.ErrCrewConfigRefused) {
		t.Errorf("refusal must match errors.Is(err, ErrCrewConfigRefused); got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"apple-container", "NetworkMode", "restricted",
		"no sidecar proxy reaches the container", "SomethingElse", "also impossible"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal error must mention %q; got:\n%s", want, msg)
		}
	}
}

// TestCrewConfigSupport_DegradedNeverBecomesAnError is the other half of the
// judgement this whole mechanism encodes: a lost capability is reported, a lost
// *containment control* is refused. If degraded fields could produce an error,
// a crew would stop starting because its TTL was unsupported.
func TestCrewConfigSupport_DegradedNeverBecomesAnError(t *testing.T) {
	s := provider.CrewConfigSupport{Degraded: []provider.DroppedField{
		{Field: "Image", Value: "crewship-cache:abc", Detail: "runs from the base image"},
	}}
	if err := s.RefusedError("apple-container"); err != nil {
		t.Fatalf("degraded-only report must not be an error, got %v", err)
	}
	if s.Empty() {
		t.Fatal("a degraded-only report is not empty — it still has to be surfaced")
	}
	msgs := s.DegradedMessages()
	if len(msgs) != 1 {
		t.Fatalf("DegradedMessages() = %v, want one line", msgs)
	}
	for _, want := range []string{"Image", "crewship-cache:abc", "runs from the base image"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("degraded message must mention %q; got %q", want, msgs[0])
		}
	}
}

// TestCrewConfigSupport_DropFindsEitherClassAndMissesNothingElse pins the
// accessor the crew read paths use to derive a setting's effective state. A
// caller asking "is this in effect?" gets the same answer for a refused field
// as for a degraded one — neither is applied — so Drop searches both. Getting
// this wrong reports a crew as enforced while the provider drops the field,
// which is the whole defect.
func TestCrewConfigSupport_DropFindsEitherClassAndMissesNothingElse(t *testing.T) {
	s := provider.CrewConfigSupport{
		Refused:  []provider.DroppedField{{Field: "Hypothetical", Detail: "cannot proceed"}},
		Degraded: []provider.DroppedField{{Field: "NetworkMode", Value: "restricted", Detail: "no proxy"}},
	}

	got, ok := s.Drop("NetworkMode")
	if !ok || got.Detail != "no proxy" {
		t.Fatalf("Drop(NetworkMode) = %+v, %v; want the degraded entry", got, ok)
	}
	if _, ok := s.Drop("Hypothetical"); !ok {
		t.Error("Drop must find refused entries too — a refused field is no more applied than a degraded one")
	}
	if _, ok := s.Drop("TTLHours"); ok {
		t.Error("Drop must not claim a field the provider never reported")
	}
	if _, ok := (provider.CrewConfigSupport{}).Drop("NetworkMode"); ok {
		t.Error("an empty report drops nothing")
	}
}

// TestCrewConfigSupport_FieldsListsBothClasses pins the summary accessor the
// providers use for their one-line log: it must not quietly omit the refused
// entries, which are the ones that matter most.
func TestCrewConfigSupport_FieldsListsBothClasses(t *testing.T) {
	s := provider.CrewConfigSupport{
		Refused:  []provider.DroppedField{{Field: "NetworkMode"}},
		Degraded: []provider.DroppedField{{Field: "Services"}, {Field: "TTLHours"}},
	}
	got := s.Fields()
	want := "NetworkMode,Services,TTLHours"
	if got != want {
		t.Errorf("Fields() = %q, want %q", got, want)
	}
}
