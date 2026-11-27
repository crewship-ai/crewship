package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

const craftImportXLSXLimit = 64 << 20

// craftImportRefusal says in plain words why the server did not import.
var craftImportRefusal = map[string]string{
	"not_xlsx":  "the file is not a readable .xlsx workbook (an encrypted or damaged file cannot be imported)",
	"too_large": "the workbook is larger than the import limits (a sheet may hold at most 1 000 000 XML elements, roughly 330 000 formula cells)",
	"truncated": "the editor would read only part of this workbook, so nothing was imported",
	"failed":    "the conversion failed",
}

// craftImport converts an .xlsx workbook on the server and publishes it as a
// new native Sheet through the ordinary initialize route. The workbook file is
// only read; an existing Sheet is never replaced (the Page must be empty).
func craftImport(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("file")
	wait, _ := cmd.Flags().GetDuration("wait")
	f, err := os.Open(name)
	if err != nil {
		return cli.WithExitCode(fmt.Errorf("cannot read the workbook file"), cli.ExitValidation)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, craftImportXLSXLimit+1))
	if err != nil || len(data) == 0 || len(data) > craftImportXLSXLimit {
		return cli.WithExitCode(fmt.Errorf("the workbook must be 1 byte to 64 MiB"), cli.ExitValidation)
	}
	c, err := pageClient()
	if err != nil {
		return err
	}
	c = c.WithContext(cmd.Context())
	base := "/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/import"
	req, err := c.NewRequest(cmd.Context(), http.MethodPost, base, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return craftTransportError("workbook upload failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return craftHTTPError(resp, "")
	}
	raw, err := craftReadJSON(resp.Body)
	var started struct {
		ID string `json:"id" yaml:"id"`
	}
	if err != nil || json.Unmarshal(raw, &started) != nil || started.ID == "" {
		return fmt.Errorf("invalid import response")
	}
	deadline := time.Now().Add(wait)
	for {
		r, e := c.Get(base + "/" + started.ID)
		if e != nil {
			return craftTransportError("import status request failed")
		}
		body, e := craftReadJSON(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK || e != nil {
			return craftHTTPError(r, "")
		}
		var st struct {
			State   string `json:"state" yaml:"state"`
			Code    string `json:"code" yaml:"code"`
			Cells   int64  `json:"cells" yaml:"cells"`
			Archive struct {
				Notes []string `json:"notes" yaml:"notes"`
			} `json:"archive" yaml:"archive"`
		}
		if json.Unmarshal(body, &st) != nil {
			return fmt.Errorf("invalid import status")
		}
		switch st.State {
		case "ready":
			for _, n := range st.Archive.Notes {
				fmt.Fprintln(cmd.ErrOrStderr(), "note:", n)
			}
			doc, e := c.Get(base + "/" + started.ID + "/document")
			if e != nil {
				return craftTransportError("import document request failed")
			}
			defer doc.Body.Close()
			if doc.StatusCode != http.StatusOK {
				return craftHTTPError(doc, "")
			}
			native, e := io.ReadAll(io.LimitReader(doc.Body, craftNativeBytesLimit+1))
			if e != nil || len(native) == 0 || len(native) > craftNativeBytesLimit {
				return fmt.Errorf("the converted document is larger than the %d MiB a Sheet may hold", craftNativeBytesLimit>>20)
			}
			return craftInitializeBytes(cmd, args, c, "gridcraft", native)
		case "failed":
			msg, ok := craftImportRefusal[st.Code]
			if !ok {
				msg = craftImportRefusal["failed"]
			}
			return cli.WithExitCode(errors.New("import refused: "+msg), cli.ExitValidation)
		case "converting":
		default:
			return fmt.Errorf("invalid import status")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the conversion is still running; stopped waiting")
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(time.Second):
		}
	}
}
