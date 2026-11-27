package craftengine

import (
	"context"
	"testing"
	"time"
)

// A one-shot run (convert, run --out) reports its exit code and the last line it wrote, every time:
// the line is read before the process is reaped, never lost to the pipe closing.
func TestWorkerKeepsExitCodeAndLastLine(t *testing.T) {
	const parts = `{"parts":[{"code":"pivot_fields","count":120}]}`
	for i := 0; i < 5; i++ {
		w, err := startTestWorker(t, wasiPrintingExit(parts+"\n", 3), "")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err = w.Wait(ctx); err == nil {
			t.Fatal("exit 3 waited as a success")
		}
		if code := w.ExitCode(); code != 3 {
			t.Fatalf("exit code %d", code)
		}
		if line := string(w.LastLine(ctx)); line != parts {
			t.Fatalf("run %d: last line %q", i, line)
		}
		cancel()
		w.Close()
	}
	w, err := startTestWorker(t, wasiPrinting("done\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = w.Wait(ctx); err != nil || w.ExitCode() != 0 || string(w.LastLine(ctx)) != "done" {
		t.Fatalf("clean run %v %d %q", err, w.ExitCode(), w.LastLine(ctx))
	}
}
