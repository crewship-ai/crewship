// crewship-gridcraft-worker runs only inside the offline, digest-pinned image.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := mooring.ServeGridCraftWorker(ctx, "/usr/local/bin/gridcraft-cli", "/scratch", os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "workbook candidate rejected")
		os.Exit(1)
	}
}
