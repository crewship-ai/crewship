package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/episodic"
)

// failingEmbedder stands in for the stage failure mode: an Ollama host
// that is up and reachable, so the embedder constructs fine, but has no
// nomic-embed-text model, so every call 404s.
type failingEmbedder struct{ err error }

func (f failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, f.err
}
func (f failingEmbedder) Dim() int      { return 768 }
func (f failingEmbedder) Model() string { return "nomic-embed-text" }

type workingEmbedder struct{}

func (workingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1}, nil
}
func (workingEmbedder) Dim() int      { return 768 }
func (workingEmbedder) Model() string { return "nomic-embed-text" }

// episodicMode() answered "vector" on `episodicEmbedder != nil`, i.e. on
// configuration alone. These pin it to evidence.
func TestEpisodicMode(t *testing.T) {
	embedErr := errors.New(`ollama http 404: model "nomic-embed-text" not found`)

	tests := []struct {
		name string
		// setup returns the embedder to stash on the Server, after any
		// calls the scenario needs to have already happened.
		setup func(t *testing.T) episodic.Embedder
		want  string
	}{
		{
			name:  "no embedder configured is sparse-only",
			setup: func(*testing.T) episodic.Embedder { return nil },
			want:  "sparse-only",
		},
		{
			name: "configured but never called is still vector",
			// No evidence of failure is not evidence of failure: a fresh
			// boot with an empty journal never calls Embed.
			setup: func(*testing.T) episodic.Embedder {
				return episodic.NewObservedEmbedder(workingEmbedder{})
			},
			want: "vector",
		},
		{
			name: "a working embedder is vector",
			setup: func(t *testing.T) episodic.Embedder {
				e := episodic.NewObservedEmbedder(workingEmbedder{})
				if _, err := e.Embed(context.Background(), "x"); err != nil {
					t.Fatalf("setup embed: %v", err)
				}
				return e
			},
			want: "vector",
		},
		{
			name: "an embedder whose calls fail is NOT vector",
			// The whole bug: this case reported "vector" for a day while
			// the index stayed empty.
			setup: func(t *testing.T) episodic.Embedder {
				e := episodic.NewObservedEmbedder(failingEmbedder{err: embedErr})
				_, _ = e.Embed(context.Background(), "x")
				return e
			},
			want: "vector-degraded",
		},
		{
			name: "recovery is reported as promptly as failure",
			setup: func(t *testing.T) episodic.Embedder {
				stub := &recoveringEmbedder{err: embedErr}
				e := episodic.NewObservedEmbedder(stub)
				_, _ = e.Embed(context.Background(), "x")
				stub.err = nil
				if _, err := e.Embed(context.Background(), "x"); err != nil {
					t.Fatalf("setup embed after recovery: %v", err)
				}
				return e
			},
			want: "vector",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{episodicEmbedder: tt.setup(t)}
			if got := s.episodicMode(); got != tt.want {
				t.Errorf("episodicMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

type recoveringEmbedder struct{ err error }

func (r *recoveringEmbedder) Embed(context.Context, string) ([]float32, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []float32{0.1}, nil
}
func (r *recoveringEmbedder) Dim() int      { return 768 }
func (r *recoveringEmbedder) Model() string { return "nomic-embed-text" }

// A mode string alone sends the operator looking in the wrong place: a
// missing model, an unreachable host and a timeout all read identically.
func TestEpisodicHealthDetail(t *testing.T) {
	embedErr := errors.New(`ollama http 404: model "nomic-embed-text" not found`)
	e := episodic.NewObservedEmbedder(failingEmbedder{err: embedErr})
	_, _ = e.Embed(context.Background(), "x")

	s := &Server{episodicEmbedder: e}
	got := s.episodicDetail()
	if got == "" {
		t.Fatal("episodicDetail() = \"\", want the embed error so the health surface says WHY")
	}
	if !strings.Contains(got, "nomic-embed-text") {
		t.Errorf("episodicDetail() = %q, want it to carry the underlying error", got)
	}

	healthy := &Server{episodicEmbedder: episodic.NewObservedEmbedder(workingEmbedder{})}
	if d := healthy.episodicDetail(); d != "" {
		t.Errorf("episodicDetail() = %q on a healthy embedder, want empty", d)
	}
	none := &Server{}
	if d := none.episodicDetail(); d != "" {
		t.Errorf("episodicDetail() = %q with no embedder, want empty", d)
	}
}

// /healthz is registered on s.mux with no auth middleware, so whatever
// episodicDetail() returns is world-readable. Go's http.Client returns a
// *url.Error, which stringifies the FULL request URL — so an unreachable
// Ollama would publish the internal host:port, and any credentials in
// KEEPER_OLLAMA_URL, to anyone who can reach the port.
func TestEpisodicDetailDoesNotLeakTheEmbedderURL(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantAbsent  []string
		wantPresent string
	}{
		{
			name: "transport error carrying host, port and credentials",
			err: errors.New(`episodic: ollama unreachable: Post "http://admin:hunter2@10.0.5.12:11434/api/embeddings": ` +
				`dial tcp 10.0.5.12:11434: connect: connection refused`),
			wantAbsent: []string{"hunter2", "admin", "10.0.5.12", "11434"},
			// The actionable half must survive — "connection refused" is
			// what tells the operator this is reachability, not a model.
			wantPresent: "connection refused",
		},
		{
			name:        "https url is redacted too",
			err:         errors.New(`episodic: ollama unreachable: Post "https://ollama.internal.corp:8443/api/embeddings": EOF`),
			wantAbsent:  []string{"ollama.internal.corp", "8443"},
			wantPresent: "EOF",
		},
		{
			name: "a model-missing error has no url and must survive intact",
			err:  errors.New(`episodic: ollama http 404: {"error":"model \"nomic-embed-text\" not found, try pulling it first"}`),
			// This is the whole reason the field exists — do not scrub it.
			wantPresent: "nomic-embed-text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := episodic.NewObservedEmbedder(failingEmbedder{err: tt.err})
			_, _ = e.Embed(context.Background(), "x")
			got := (&Server{episodicEmbedder: e}).episodicDetail()

			for _, leak := range tt.wantAbsent {
				if strings.Contains(got, leak) {
					t.Errorf("episodicDetail() = %q, must not contain %q — /healthz is unauthenticated", got, leak)
				}
			}
			if tt.wantPresent != "" && !strings.Contains(got, tt.wantPresent) {
				t.Errorf("episodicDetail() = %q, want it to keep %q so the error stays actionable", got, tt.wantPresent)
			}
		})
	}
}
