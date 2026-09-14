package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// DecodeProjectJSON refuses duplicate keys as well as unknown fields, unlike
// encoding/json's default last-key-wins behavior. No code is executed.
func DecodeProjectJSON(b []byte, out any) error {
	if !json.Valid(b) {
		return errors.New("invalid JSON document")
	}
	return decodeProjectDocument(b, out)
}

func decodeProjectDocument(b []byte, out any) error {
	if len(b) > MaxTransferBytes {
		return errors.New("Page bundle exceeds size limit")
	}
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return err
	}
	if err := validateProjectNode(&node, 0); err != nil {
		return err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one document")
	}
	return nil
}

func ParseProjectTransfer(r io.Reader) (*TransferImport, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxTransferBytes+1))
	if err != nil {
		return nil, err
	}
	var req TransferImport
	if err := decodeProjectDocument(b, &req); err != nil {
		return nil, err
	}
	if req.Format != TransferV2 {
		return nil, errors.New("unsupported project bundle format")
	}
	if _, err := req.Project.MarshalYAMLSource(); err != nil {
		return nil, err
	}
	return &req, nil
}

func MarshalProjectTransfer(bundle TransferBundle) ([]byte, error) {
	if bundle.Format != TransferV2 {
		return nil, errors.New("unsupported project bundle format")
	}
	if _, err := bundle.Project.MarshalYAMLSource(); err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxTransferBytes {
		return nil, errors.New("encoded Page bundle exceeds size limit")
	}
	return b, nil
}
