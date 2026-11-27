package main

import (
	"bytes"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftsession"
)

func TestCraftEngineWorkerModeIsOnlyEnteredByItsFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if handled, _ := runCraftEngineWorker([]string{"crewship", "version"}, nil, &out, &errb); handled {
		t.Fatal("an ordinary command entered worker mode")
	}
	if handled, _ := runCraftEngineWorker([]string{"crewship"}, nil, &out, &errb); handled {
		t.Fatal("no arguments entered worker mode")
	}
	// the flag alone has no module: the worker must refuse, not start a server
	handled, code := runCraftEngineWorker([]string{"crewship", craftsession.WorkerFlag}, nil, &out, &errb)
	if !handled || code == 0 {
		t.Fatalf("worker mode without a module: handled=%v code=%d", handled, code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout carries %q", out.String())
	}
}
