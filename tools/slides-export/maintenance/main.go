// Local operational cleanup only: never registered as an HTTP, MCP or run CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/crewship-ai/crewship/internal/slidesexport"
)

func main() {
	journal := flag.String("journal", "", "private export cleanup journal directory")
	owner := flag.String("owner", "", "original installation owner identity")
	name := flag.String("ack-name", "", "one retained uncertain intent name to acknowledge")
	reference := flag.String("reference", "", "local operational disposition identifier")
	quiescent := flag.Bool("daemon-quiescent", false, "operator independently confirmed no delayed create can arrive")
	flag.Parse()
	if *journal == "" || *owner == "" || flag.NArg() != 0 || (*name == "" && (*reference != "" || *quiescent)) || (*name != "" && (*reference == "" || !*quiescent)) {
		fmt.Fprintln(os.Stderr, "explicit journal/owner and complete acknowledgement disposition required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := &slidesexport.Runner{JournalDir: *journal, OwnerID: *owner}
	defer runner.Close()
	var err error
	if *name == "" {
		err = runner.Reconcile(ctx)
	} else {
		err = runner.AcknowledgeCleanup(ctx, *name, *reference, *quiescent)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "export cleanup remains unresolved; preserve the journal and independently verify daemon quiescence")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "export cleanup settled")
}
