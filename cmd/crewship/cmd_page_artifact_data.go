package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

func newArtifactDataCommand() *cobra.Command {
	root := &cobra.Command{Use: "data", Short: "Read and atomically update declared dashboard datasets"}
	for _, operation := range []string{"get", "collections"} {
		operation := operation
		args := cobra.ExactArgs(2)
		use := operation + " <slug> <collection>"
		short := "Read every document of a declared collection at an exact code version"
		if operation == "collections" {
			args = cobra.ExactArgs(1)
			use = "collections <slug>"
			short = "List declared collections that hold documents at an exact code version"
		}
		cmd := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			version, _ := cmd.Flags().GetInt64("version")
			if version < 1 || version > artifacts.MaxDataVersion {
				return fmt.Errorf("--version must be an exact positive code version")
			}
			path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact/data"
			if operation == "get" {
				path += "/" + pagePathEscape(args[1])
			}
			return artifactCLIRequest(cmd, http.MethodGet, path+"?version="+strconv.FormatInt(version, 10), nil)
		}}
		cmd.Example = "  crewship page artifact data " + operation + " sales-dashboard sales --version 1"
		if operation == "collections" {
			cmd.Example = "  crewship page artifact data collections sales-dashboard --version 1"
		}
		cmd.Flags().Int64("version", 0, "Exact code version declaring the collection (required)")
		root.AddCommand(cmd)
	}
	batch := &cobra.Command{Use: "batch <slug> <collection>", Short: "Apply {base_version,documents:[{id,ifVersion,value}]} atomically", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			return fmt.Errorf("--file is required")
		}
		var reader io.Reader = cmd.InOrStdin()
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		raw, err := io.ReadAll(io.LimitReader(reader, artifacts.MaxDataBatchBytes+1))
		if err != nil {
			return err
		}
		body, err := artifacts.DecodeDataBatch(raw)
		if err != nil {
			return err
		}
		return artifactCLIRequest(cmd, http.MethodPost, "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/data/"+pagePathEscape(args[1]), body)
	}}
	batch.Long = `Apply up to 50 document writes atomically. base_version is the current code
version; ifVersion 0 creates a document and an update gives the document's
current version. One stale ifVersion fails the whole batch with 409
artifact_data_conflict and nothing is written; the CLI never retries.`
	batch.Example = `  echo '{"base_version":1,"documents":[{"id":"north","ifVersion":0,"value":{"revenue":1250}}]}' \
    | crewship page artifact data batch sales-dashboard sales --file -`
	batch.Flags().String("file", "", "Bounded JSON batch file; - reads stdin (required)")
	root.AddCommand(batch)
	root.Long = `Read and write the JSON documents of a dashboard artifact.

A code version declares its collections in dashboard.json, for example
{"schema_version":1,"collections":["sales"]}. Data is versioned per document
and never creates a code version. Requires the artifact_projects and
artifact_dashboard_data workspace feature flags.`
	root.Example = `  crewship page artifact data collections sales-dashboard --version 1
  crewship page artifact data get sales-dashboard sales --version 1
  crewship page artifact data batch sales-dashboard sales --file batch.json`
	return root
}

func artifactDataHumanRequest(cmd *cobra.Command, method, path string, body any) error {
	client, err := pageClient()
	if err != nil {
		return err
	}
	var response *http.Response
	if method == http.MethodGet {
		response, err = client.Get(path)
	} else {
		raw, encodeErr := artifacts.MarshalDataJSON(body)
		if encodeErr != nil {
			return encodeErr
		}
		response, err = client.Post(path, bytes.NewReader(raw))
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := pageCheckError(response); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, artifacts.MaxDataResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > artifacts.MaxDataResponseBytes || !json.Valid(raw) {
		return fmt.Errorf("invalid or oversized dataset response")
	}
	return emitArtifactData(cmd, raw)
}
func emitArtifactData(cmd *cobra.Command, raw []byte) error {
	f := resolvedFormatter(cmd)
	doc := cli.RawJSON(raw)
	switch f.Format {
	case "yaml":
		return f.YAML(doc)
	case "ndjson":
		return f.NDJSON(doc)
	default:
		return f.JSON(doc)
	}
}
