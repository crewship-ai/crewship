// Package design hosts the standard Yjs engine without a new long-running
// service. Go remains the only authority for credentials, ACLs and persistence.
package design

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

//go:generate node build.mjs

//go:embed engine.cjs
var engine string

var ErrUnavailable = errors.New("design Yjs engine unavailable")
var ErrInvalid = errors.New("invalid design update")

type Request struct {
	State      []byte          `json:"state,omitempty"`
	Frame      []byte          `json:"frame,omitempty"`
	Replace    []byte          `json:"replace,omitempty"`
	Operations json.RawMessage `json:"operations,omitempty"`
}
type Result struct {
	Hello    []byte            `json:"hello"`
	State    []byte            `json:"state"`
	Update   []byte            `json:"update"`
	Frame    []byte            `json:"frame"`
	Response []byte            `json:"response"`
	Nodes    []json.RawMessage `json:"nodes"`
	Order    []string          `json:"order"`
}

// Run executes one bounded standard Yjs operation. Node is an existing build/
// application runtime, not a new host service. No caller chooses executable code.
func Run(ctx context.Context, r Request) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	nodePath, lookupErr := exec.LookPath("node")
	if lookupErr != nil {
		return Result{}, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, nodePath, "--max-old-space-size=96", "-e", engine)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = bytes.NewReader(raw)
	var output, stderr bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || ctx.Err() != nil {
			return Result{}, ErrUnavailable
		}
		return Result{}, fmt.Errorf("%w: %s", ErrInvalid, stderr.String())
	}
	var result Result
	if err = json.Unmarshal(output.Bytes(), &result); err != nil {
		return Result{}, ErrUnavailable
	}
	return result, nil
}
