//go:build linux

// crewship-restricted-runner is the fixed bootstrap of the isolated text runtime.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "restricted worker failed")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("mode required")
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "hold":
		if os.Getuid() != 1002 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		return restrictedruntime.RunLeaseGuard(ctx)
	case "lease":
		if os.Getuid() != 1002 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		var expires time.Time
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 4096)).Decode(&expires); err != nil {
			return err
		}
		return restrictedruntime.RenewLease(expires)
	case "broker":
		if os.Getuid() != 1002 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		return restrictedruntime.RunHTTPBroker(ctx, os.Stdin, os.Stdout)
	case "launch":
		if os.Getuid() != 1001 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		var b restrictedruntime.Bootstrap
		d := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20))
		d.DisallowUnknownFields()
		if err := d.Decode(&b); err != nil {
			return err
		}
		// This production image exposes only the text worker. No arbitrary command,
		// persistent file delivery or agent-visible provider key is supported.
		if len(b.Files) != 0 || len(b.Command) != 3 || b.Command[0] != "/opt/crewship-runner" || b.Command[1] != "responses" || len(b.Env) != 2 {
			return errors.New("launch policy")
		}
		if b.Env["CREWSHIP_BROKER_URL"] != "http://127.0.0.1:9121" || len(b.Env["CREWSHIP_BROKER_TOKEN"]) != 64 {
			return errors.New("broker policy")
		}
		exe, err := exec.LookPath(b.Command[0])
		if err != nil {
			return err
		}
		env := []string{"HOME=/home/agent", "PATH=/usr/bin:/bin", "LANG=C", "CREWSHIP_BROKER_URL=" + b.Env["CREWSHIP_BROKER_URL"], "CREWSHIP_BROKER_TOKEN=" + b.Env["CREWSHIP_BROKER_TOKEN"]}
		return syscall.Exec(exe, b.Command, env)
	case "responses":
		if os.Getuid() != 1001 || len(os.Args) != 3 {
			return errors.New("identity")
		}
		return responses(ctx, os.Args[2], os.Stdout)
	}
	return errors.New("unsupported mode")
}

// responses emits bounded JSON lines, never unclassified provider metadata.
func responses(ctx context.Context, body string, output io.Writer) error {
	if len(body) > 128<<10 || !json.Valid([]byte(body)) {
		return errors.New("request")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:9121/v1/responses", bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("CREWSHIP_BROKER_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("provider denied")
	}
	return collectResponses(resp.Body, output)
}
func collectResponses(input io.Reader, output io.Writer) error {
	limited := &io.LimitedReader{R: input, N: (1 << 20) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(output)
	var data strings.Builder
	completed := false
	size := 0
	process := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := data.String()
		data.Reset()
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Status string `json:"status"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			return errors.New("invalid event")
		}
		switch event.Type {
		case "response.output_text.delta":
			if completed {
				return errors.New("late output")
			}
			size += len(event.Delta)
			if size > 128<<10 {
				return errors.New("output limit")
			}
			return encoder.Encode(map[string]string{"type": "text", "text": event.Delta})
		case "response.completed":
			if completed || event.Response.Status != "completed" {
				return errors.New("invalid completion")
			}
			completed = true
		case "response.failed", "response.incomplete", "error":
			return errors.New("provider incomplete")
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := process(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := process(); err != nil {
		return err
	}
	if limited.N <= 0 {
		return errors.New("response limit")
	}
	if !completed {
		return errors.New("missing completion")
	}
	return encoder.Encode(map[string]string{"type": "done"})
}
