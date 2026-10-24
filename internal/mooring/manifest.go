// Package mooring implements the small manifest-driven JSON document profile.
// Identity and authority remain responsibilities of the host, not the editor.
package mooring

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed manifests/*.json
var manifests embed.FS

type Command struct {
	Name        string          `json:"name"`
	Effect      string          `json:"effect"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type Editor struct {
	Entry string `json:"entry"`
}
type Manifest struct {
	ID              string          `json:"id"`
	ProtocolVersion string          `json:"protocol_version"`
	Format          string          `json:"format"`
	Editor          Editor          `json:"editor"`
	InitialState    json.RawMessage `json:"initial_state"`
	Commands        []Command       `json:"commands"`
}

// Applications loads only operator-installed compiled manifests; documents cannot
// install commands, code, URLs or schemas. No application ID is interpreted here.
func Applications() []Manifest {
	entries, _ := manifests.ReadDir("manifests")
	result := []Manifest{}
	for _, entry := range entries {
		raw, _ := manifests.ReadFile("manifests/" + entry.Name())
		var m Manifest
		if json.Unmarshal(raw, &m) != nil {
			panic("invalid compiled Mooring manifest")
		}
		result = append(result, m)
	}
	return result
}
func Application(id string) (Manifest, error) {
	for _, m := range Applications() {
		if m.ID == id {
			return m, nil
		}
	}
	return Manifest{}, errors.New("application unavailable")
}
func (m Manifest) Apply(command string, args json.RawMessage) (json.RawMessage, error) {
	for _, c := range m.Commands {
		if c.Name == command && c.Effect == "replace-state" {
			return m.Validate(command, args)
		}
	}
	return nil, errors.New("unsupported command effect")
}
func (m Manifest) Validate(command string, args json.RawMessage) (json.RawMessage, error) {
	for _, c := range m.Commands {
		if c.Name != command {
			continue
		}
		compiler := jsonschema.NewCompiler()
		compiler.LoadURL = func(string) (io.ReadCloser, error) { return nil, errors.New("external schema reference refused") }
		if err := compiler.AddResource("command.json", bytes.NewReader(c.InputSchema)); err != nil {
			return nil, err
		}
		schema, err := compiler.Compile("command.json")
		if err != nil {
			return nil, err
		}
		var value any
		if err = json.Unmarshal(args, &value); err != nil {
			return nil, err
		}
		if err = schema.Validate(value); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		return json.Marshal(value)
	}
	return nil, errors.New("undeclared command")
}
