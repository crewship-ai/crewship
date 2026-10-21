package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/documents"
)

func readRunDocumentTarget(path string) (*documents.IterationIntent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(raw) > 1024 {
		return nil, errors.New("Document target must be closed JSON of at most 1024 bytes")
	}
	var target documents.IterationIntent
	if json.Unmarshal(raw, &target) != nil {
		return nil, errors.New("Document target must be a JSON object")
	}
	return &target, nil
}
func sendRunMessage(ws *cli.WSClient, channel, chat, prompt string, maxTurns int, targets ...*documents.IterationIntent) error {
	if len(targets) > 0 && targets[0] != nil {
		return ws.SendMessageWithMetadata(channel, chat, prompt, maxTurns, map[string]any{"document_iteration_intent": targets[0]})
	}
	return ws.SendMessage(channel, chat, prompt, maxTurns)
}
