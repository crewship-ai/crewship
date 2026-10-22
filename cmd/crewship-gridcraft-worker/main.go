// crewship-gridcraft-worker serves a fixed private listener inside the offline image.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/documents"
)

func run() error {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		client, err := appengine.NewWorkerClient("/run/worker/service.sock", "/run/secrets/service-token")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return client.Health(ctx)
	}
	if len(os.Args) != 1 {
		return fmt.Errorf("invalid worker arguments")
	}

	secret, err := documents.ReadServiceSecret("/run/secrets/service-token")
	if err != nil {
		return err
	}
	handler, err := appengine.NewWorkerHandler(secret, appengine.OfflineRunner{Binary: "/usr/local/bin/gridcraft-cli", Scratch: "/scratch"}, 1)
	if err != nil {
		return err
	}
	stop, err := documents.ServePrivateUnix("/run/worker/service.sock", handler)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	<-ctx.Done()
	drain, done := context.WithTimeout(context.Background(), 25*time.Second)
	defer done()
	return stop(drain)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "workbook worker unavailable")
		os.Exit(1)
	}
}
