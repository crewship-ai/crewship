// hash-check is a repository contract harness, not an agent or HTTP API.
package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"

	"github.com/crewship-ai/crewship/internal/slidesexport"
)

func main() {
	limit := slidesexport.MaxReportBytes
	if len(os.Args) > 1 && os.Args[1] == "frame" {
		limit = slidesexport.MaxFrameBytes
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, int64(limit+1)))
	if err != nil || len(data) > limit {
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "frame" {
		if len(os.Args) != 7 {
			os.Exit(1)
		}
		revision, err := strconv.ParseInt(os.Args[4], 10, 64)
		if err != nil {
			os.Exit(1)
		}
		b := slidesexport.Binding{Mode: os.Args[2], PageID: os.Args[3], Revision: revision, ContentHash: os.Args[5], ReportHash: os.Args[6]}
		frame, err := slidesexport.DecodeFrame(data, b)
		if err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"report_hash": frame.ReportHash, "binary_bytes": len(frame.Binary), "binary_sha256": frame.SHA256, "mime": frame.MIME})
		return
	}
	var cases []json.RawMessage
	if json.Unmarshal(data, &cases) != nil {
		os.Exit(1)
	}
	results := []map[string]string{}
	for _, value := range cases {
		hash, err := slidesexport.ReportHash(value)
		if err != nil {
			results = append(results, map[string]string{"error": "refused"})
		} else {
			results = append(results, map[string]string{"hash": hash})
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(results)
}
