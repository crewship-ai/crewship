package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
)

// clientCrewFileIO backs crewFileIO with the live CLI client, hitting the same
// `/crews/{id}/files/{download,save}` endpoints as `crewship crew files`. No
// second storage channel — inline/materialize ride the audited crew-file path.
type clientCrewFileIO struct {
	ctx    context.Context
	client *cli.Client
}

func newClientCrewFileIO(ctx context.Context, client *cli.Client) *clientCrewFileIO {
	return &clientCrewFileIO{ctx: ctx, client: client}
}

func (c *clientCrewFileIO) download(crewID, crewPath string) ([]byte, bool, error) {
	resp, err := c.client.Get("/api/v1/crews/" + url.PathEscape(crewID) +
		"/files/download?path=" + url.QueryEscape(crewPath))
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil // absent, not an error
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read file body: %w", err)
	}
	return data, true, nil
}

func (c *clientCrewFileIO) save(crewID, crewPath string, data []byte) error {
	return putBytes(c.ctx, c.client,
		"/api/v1/crews/"+url.PathEscape(crewID)+"/files/save?path="+url.QueryEscape(crewPath),
		bytes.NewReader(data))
}
