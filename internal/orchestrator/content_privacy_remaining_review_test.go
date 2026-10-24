package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestReviewPrivateExecCreationAndGateDiagnosticsStayOpaque(t *testing.T) {
	const marker = "synthetic-private-create-diagnostic"
	for _, mode := range []string{"create", "gate"} {
		t.Run(mode, func(t *testing.T) {
			container := covNewRunContainer(covRunOpts{})
			original := container.route
			container.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
				if mode == "create" && len(cfg.Cmd) > 0 && cfg.Cmd[0] == "stdbuf" {
					return nil, errors.New(marker)
				}
				return original(cfg)
			}
			var log bytes.Buffer
			j := &covJournal{}
			o := New(container, newMemState(), slog.New(slog.NewJSONHandler(&log, nil)))
			o.SetJournal(j)
			req := covRunReq().WithStrictContentPrivacy()
			if mode == "gate" {
				req.ExecGate = func(context.Context) error { return errors.New(marker) }
			}
			err := o.RunAgent(context.Background(), req, nil)
			if !errors.Is(err, ErrPrivateRunUnqualified) {
				t.Fatal("fixture failed to reach opaque failure", err)
			}
			if strings.Contains(log.String(), marker) {
				t.Error("private diagnostic reached host log")
			}
			for _, entry := range j.byType("exec.command") {
				if strings.Contains(entry.Summary, marker) || strings.Contains(fmt.Sprint(entry.Payload), marker) {
					t.Error("private diagnostic persisted in journal")
				}
			}
		})
	}
}
