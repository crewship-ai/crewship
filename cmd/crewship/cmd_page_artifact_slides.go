package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

const artifactSlidesPDFBytes = 8 << 20
const artifactSlidesResponseBytes = 16 << 20
const artifactSlidesMaxVersion int64 = 9007199254740991

func newArtifactSlidesCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, export := range []bool{false, true} {
		export := export
		name, short := "slides", "Read an authorized Slides projection at an exact code version"
		if export {
			name, short = "export-pdf", "Export an authorized Slides version to a local PDF file"
		}
		command := &cobra.Command{Use: name + " <slug-or-id>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			version, _ := cmd.Flags().GetInt64("version")
			if version < 1 || version > artifactSlidesMaxVersion {
				return errors.New("--version must be an exact positive safe code version")
			}
			out := ""
			suffix := "slides"
			if export {
				out, _ = cmd.Flags().GetString("out")
				if out == "" || out == "-" {
					return errors.New("--out must name a local PDF file")
				}
				suffix = "export.pdf"
			}
			path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact/" + suffix + "?version=" + strconv.FormatInt(version, 10)
			if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
				return artifactCLIRequestWithResultTransport(cmd, "GET", path, nil, nil, func(raw []byte) error {
					if !export {
						return pageEmitMachine(resolvedFormatter(cmd), raw, "{}")
					}
					pdf, err := decodeArtifactSlidesPDF(raw, version)
					if err != nil {
						return err
					}
					return saveArtifactSlidesPDF(cmd, out, pdf, version)
				})
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			response, err := client.WithContext(cmd.Context()).Get(path)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err := pageCheckError(response); err != nil {
				return err
			}
			limit := artifactSlidesResponseBytes
			if export {
				limit = artifactSlidesPDFBytes
				media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
				if err != nil || media != "application/pdf" {
					return errors.New("Slides export did not return application/pdf")
				}
			}
			raw, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
			if err != nil {
				return err
			}
			if len(raw) > limit {
				return errors.New("Slides response exceeds limit")
			}
			if export {
				return saveArtifactSlidesPDF(cmd, out, raw, version)
			}
			if !json.Valid(raw) {
				return errors.New("invalid Slides projection JSON")
			}
			return pageEmitMachine(resolvedFormatter(cmd), raw, "{}")
		}}
		command.Long = `Read the Slides projection of an exact artifact code version: the deck
(slide order, positioned elements, theme) and the element anchors that
comments can use. The version needs deck.json and one slides/<id>.html per
slide; other content returns 422 artifact_slides_unsupported. Requires the
artifact_projects and artifact_slides workspace feature flags. Owners can
read any retained version; shared readers only the shared version.`
		command.Example = "  crewship page artifact slides quarterly-deck --version 1 -f json"
		if export {
			command.Long = `Export the deck of an exact artifact code version to a local PDF file.
The file is written atomically and standard output carries a JSON receipt
(path, media_type, code_version, byte_length, sha256), never PDF bytes.
--out - is rejected. Same feature flags and version access as slides.`
			command.Example = "  crewship page artifact export-pdf quarterly-deck --version 1 --out deck.pdf"
		}
		command.Flags().Int64("version", 0, "Exact immutable code version (required)")
		if export {
			command.Flags().String("out", "", "Local PDF output path (required; never sent to the server)")
		}
		commands = append(commands, command)
	}
	return commands
}

// A protected run receives bytes through the existing original-run gateway.
// Validate the entire closed DTO before creating even a temporary local file.
func decodeArtifactSlidesPDF(raw []byte, version int64) ([]byte, error) {
	invalid := errors.New("invalid or mismatched Slides PDF response")
	if len(raw) > artifactSlidesResponseBytes || validateArtifactManifestJSON(raw) != nil {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 6 {
		return nil, invalid
	}
	for _, key := range []string{"media_type", "encoding", "code_version", "byte_length", "sha256", "data"} {
		if _, ok := fields[key]; !ok {
			return nil, invalid
		}
	}
	var dto struct {
		MediaType   string `json:"media_type" yaml:"media_type"`
		Encoding    string `json:"encoding" yaml:"encoding"`
		CodeVersion int64  `json:"code_version" yaml:"code_version"`
		ByteLength  int    `json:"byte_length" yaml:"byte_length"`
		SHA256      string `json:"sha256" yaml:"sha256"`
		Data        string `json:"data" yaml:"data"`
	}
	if json.Unmarshal(raw, &dto) != nil || dto.MediaType != "application/pdf" || dto.Encoding != "base64" || dto.CodeVersion != version || version < 1 || version > artifactSlidesMaxVersion || dto.ByteLength < 5 || dto.ByteLength > artifactSlidesPDFBytes || len(dto.Data) > base64.StdEncoding.EncodedLen(artifactSlidesPDFBytes) {
		return nil, invalid
	}
	pdf, err := base64.StdEncoding.Strict().DecodeString(dto.Data)
	if err != nil || len(pdf) != dto.ByteLength || !bytes.HasPrefix(pdf, []byte("%PDF-")) || base64.StdEncoding.EncodeToString(pdf) != dto.Data {
		return nil, invalid
	}
	sum := sha256.Sum256(pdf)
	if dto.SHA256 != hex.EncodeToString(sum[:]) {
		return nil, invalid
	}
	return pdf, nil
}

func saveArtifactSlidesPDF(cmd *cobra.Command, path string, pdf []byte, version int64) error {
	if len(pdf) > artifactSlidesPDFBytes || !bytes.HasPrefix(pdf, []byte("%PDF-")) || version < 1 || version > artifactSlidesMaxVersion {
		return errors.New("invalid Slides PDF bytes or selected version")
	}
	output, err := cli.NewAtomicFile(path)
	if err != nil {
		return err
	}
	defer output.Close()
	if _, err := output.Write(pdf); err != nil {
		return err
	}
	if err := output.Commit(); err != nil {
		return err
	}
	sum := sha256.Sum256(pdf)
	receipt := struct {
		Path        string `json:"path" yaml:"path"`
		MediaType   string `json:"media_type" yaml:"media_type"`
		CodeVersion int64  `json:"code_version" yaml:"code_version"`
		ByteLength  int    `json:"byte_length" yaml:"byte_length"`
		SHA256      string `json:"sha256" yaml:"sha256"`
	}{path, "application/pdf", version, len(pdf), hex.EncodeToString(sum[:])}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode PDF receipt: %w", err)
	}
	return pageEmitMachine(resolvedFormatter(cmd), raw, "{}")
}
