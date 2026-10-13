package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

// RetainedAnalysis reconstructs semantic CLI facts without replaying live
// actions, publishing messages or charging usage again. Metadata is bounded
// and already redacted by protected capture before it reaches this parser.
type RetainedAnalysis struct {
	Failed           bool
	Unknown          bool
	TerminalObserved bool
	ErrorMessage     string
	Metadata         map[string]any
}

func (o *Orchestrator) AnalyzeRetainedRun(ctx context.Context, run RunState) (RetainedAnalysis, error) {
	analysis := RetainedAnalysis{Metadata: map[string]any{}}
	directory, err := o.validatedHostRunDirectory(run)
	if err != nil {
		return analysis, err
	}
	adapter := getAdapter(run.Output.Adapter)
	parse := adapter.ParseStreamLine
	if factory, ok := adapter.(streamLineParserFactory); ok {
		parse = factory.NewStreamLineParser()
	}
	var inBand inBandFailure
	handler := func(event AgentEvent) {
		inBand.observe(event)
		metadata, _ := event.Metadata.(map[string]any)
		if event.Type == "result" {
			analysis.TerminalObserved = true
			for key, value := range metadata {
				analysis.Metadata[key] = value
			}
		}
		if event.Type == "system" {
			for _, key := range []string{"model", "session_id", "thread_id", "claude_code_version", "apiKeySource", "permissionMode"} {
				if value, ok := metadata[key]; ok {
					analysis.Metadata[key] = value
				}
			}
		}
	}
	decoder, err := runoutput.NewLineDecoder(16*1024*1024, func(line runoutput.Line) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if line.Stream == "stdout" && adapter.UseStreamJSON() {
			parse(line.Data, handler)
		}
		data, err := json.Marshal(analysis.Metadata)
		if err != nil {
			return err
		}
		if len(data) > 256*1024 {
			return errors.New("retained semantic metadata exceeds limit")
		}
		return nil
	})
	if err != nil {
		return analysis, err
	}
	var terminal *runoutput.Result
	_, complete, err := runoutput.ReadCommitted(filepath.Join(directory, "output"), 0, func(record runoutput.Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := decoder.Push(record); err != nil {
			return err
		}
		if record.Kind == "exit" {
			terminal = &runoutput.Result{ExitCode: record.ExitCode, Reason: record.Reason}
		}
		return nil
	})
	if err != nil {
		return analysis, err
	}
	if !complete || terminal == nil {
		return analysis, errors.New("retained semantic analysis requires validated terminal output")
	}
	analysis.Failed = inBand.seen || terminal.ExitCode != 0 || terminal.Reason != "exited"
	if cause := inBand.Err(); cause != nil {
		analysis.ErrorMessage = cause.Error()
		analysis.Metadata["in_band_error"] = true
		analysis.Metadata["error_subtype"] = inBand.subtype
	}
	// Structured adapters should report a terminal result. Unknown success is
	// parked for reconciliation rather than fabricating a successful envelope.
	analysis.Unknown = adapter.UseStreamJSON() && !analysis.TerminalObserved && !analysis.Failed
	analysis.Metadata["semantic_terminal_observed"] = analysis.TerminalObserved
	if analysis.Unknown {
		analysis.Metadata["terminal_envelope_missing"] = true
	}
	return analysis, nil
}
