package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
)

func seedLiveCallback(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	// Rotate only this demo's callbacks; stale tokens should not accumulate.
	var previous pageWebhooksJSON
	if err := verifyBusinessGet(client, "/api/v1/pages/"+pagePathEscape("demo-live")+"/webhooks", &previous); err != nil {
		return err
	}
	resp, err := client.Post("/api/v1/pages/"+pagePathEscape("demo-live")+"/webhooks", map[string]string{"panel": "memory", "name": "demo-live-monitor"})
	if err != nil {
		return err
	}
	if err = cli.CheckError(resp); err != nil {
		return err
	}
	var hook pageWebhookJSON
	if err = cli.ReadJSON(resp, &hook); err != nil {
		return err
	}
	if hook.Token == "" {
		return fmt.Errorf("live monitor webhook returned no token")
	}
	payload, err := json.Marshal(map[string]string{"url": "http://127.0.0.1:9119/page-webhooks/" + hook.Token})
	if err != nil {
		return err
	}
	// Do not log the body or callback URL: the token grants one-panel writes.
	if err = putBytes(ctx, client, crewFileSavePath(crewIDs["ops"], "shared/demo/business/live-private.json"), bytes.NewReader(payload)); err != nil {
		return fmt.Errorf("deliver live collector callback: %w", err)
	}
	for _, old := range previous.Webhooks {
		if old.Name != "demo-live-monitor" || !old.Live {
			continue
		}
		r, e := client.Delete("/api/v1/pages/" + pagePathEscape("demo-live") + "/webhooks/" + url.PathEscape(old.ID))
		if e != nil {
			return e
		}
		e = cli.CheckError(r)
		r.Body.Close()
		if e != nil {
			return e
		}
	}

	fmt.Fprintln(os.Stderr, "  + Live monitor callback configured (one-panel scope)")
	return nil
}
