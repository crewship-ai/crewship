package restricteddispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

type textFixtureSession struct {
	output string
	done   chan struct{}
}

func (s *textFixtureSession) Output(context.Context) (string, error) { return s.output, nil }
func (s *textFixtureSession) Done() <-chan struct{}                  { return s.done }
func (s *textFixtureSession) Stop(string)                            {}
func TestTextRunnerRechecksAfterQueueAndBeforeEachDelivery(t *testing.T) {
	for _, stage := range []string{"before-builder", "queued", "delivery"} {
		t.Run(stage, func(t *testing.T) {
			a := providerFixture(t)
			if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='a'`); err != nil {
				t.Fatal(err)
			}
			setRights(t, a, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "agent", ID: "a", Operation: "chat"}})
			launched := 0
			delivered := []string{}
			runner := TextRunner{Authority: a, MaxOutputTokens: 128}
			runner.StartSession = func(ctx context.Context, handle string) (TextSession, error) {
				if stage == "queued" {
					setRights(t, a, "h1", nil)
				}
				if _, err := a.Store.Resolve(ctx, handle); err != nil {
					return nil, err
				}
				launched++
				done := make(chan struct{})
				close(done)
				return &textFixtureSession{"{\"type\":\"text\",\"text\":\"first\"}\n{\"type\":\"text\",\"text\":\"second\"}\n{\"type\":\"done\"}\n", done}, nil
			}
			if stage == "before-builder" {
				setRights(t, a, "h1", nil)
			}
			err := runner.Execute(t.Context(), "h1", "w", "c1", "private", func(kind, text string) error {
				delivered = append(delivered, kind+":"+text)
				if stage == "delivery" {
					setRights(t, a, "h1", nil)
				}
				return nil
			})
			if err == nil {
				t.Fatal("revoked run succeeded")
			}
			if stage == "delivery" {
				if launched != 1 || strings.Join(delivered, ",") != "text:first" {
					t.Fatalf("delivery %+v launched %d", delivered, launched)
				}
			} else if launched != 0 || len(delivered) != 0 {
				t.Fatalf("ran after revocation launched=%d delivered=%v", launched, delivered)
			}
			if stage != "before-builder" {
				var state string
				if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT state FROM access_attempt_outcomes`).Scan(&state); err != nil || state != "failed" {
					t.Fatalf("failed outcome state=%q err=%v", state, err)
				}
			}
			if stage == "before-builder" {
				var count int
				if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_launches`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("builder persisted launch %d %v", count, err)
				}
			}
		})
	}
}
