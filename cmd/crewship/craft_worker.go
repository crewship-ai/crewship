package main

import (
	"io"

	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/craftsession"
)

// runCraftEngineWorker serves the internal engine-worker mode: the server
// binary re-executed with craftsession.WorkerFlag runs one document engine on
// its stdin and stdout and nothing else (no database, no configuration).
func runCraftEngineWorker(args []string, stdin io.Reader, stdout, stderr io.Writer) (handled bool, code int) {
	if len(args) < 2 || args[1] != craftsession.WorkerFlag {
		return false, 0
	}
	return true, craftengine.WorkerMain(args[2:], stdin, stdout, stderr)
}
