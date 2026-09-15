package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Routine detail `files` (#2560): what a routine actually runs, with the
// truth from the author crew's shared volume. The projection is pure
// (pipeline.DescribeFiles); this file adds the I/O — presence, size,
// timestamp, header comment — through the same crewshipd IPC endpoints the
// Files panel and `routine export --scripts` read, bounded so a slow or
// absent container can never stall or fail the detail request.

const (
	// pipelineFilesMaxIO caps how many files one detail request stats and
	// reads. Rows past the cap stay present:false.
	pipelineFilesMaxIO = 25
	// pipelineFilesListTimeout bounds the one recursive listing of the share.
	pipelineFilesListTimeout = 3 * time.Second
	// pipelineFilesReadTimeout bounds each header read.
	pipelineFilesReadTimeout = 1500 * time.Millisecond
	// pipelineFilesBudget bounds the whole enrichment: once spent, the
	// remaining present files keep their size/time and lose only the
	// description.
	pipelineFilesBudget = 6 * time.Second
	// pipelineFilesHeaderBytes is how much of a file the description reader
	// fetches — a header comment lives in the first few kilobytes.
	pipelineFilesHeaderBytes = 8 << 10
)

// crewFileReader is the slice of crewshipd's file IPC the routine detail
// needs. An interface so tests can stand in a fake; production wires
// ipcCrewFileReader over the sidecar's Unix socket.
type crewFileReader interface {
	// ListShared lists the crew's /crew/shared tree recursively.
	ListShared(ctx context.Context, crewID string) ([]provider.FileInfo, error)
	// ReadShared returns at most limit bytes of one file under /crew/shared,
	// addressed relative to the share ("scripts/x.py").
	ReadShared(ctx context.Context, crewID, relPath string, limit int64) ([]byte, error)
}

// SetCrewFileReader wires the crew shared-volume reader the routine detail's
// `files` enrichment uses. nil (tests, a deployment without the sidecar
// socket) leaves every file present:false.
func (h *PipelineHandler) SetCrewFileReader(r crewFileReader) { h.crewFiles = r }

// ipcCrewFileReader talks to crewshipd over its IPC socket — the same
// transport ProxyHandler uses for the Files panel.
type ipcCrewFileReader struct {
	client *http.Client
	base   string
}

func newIPCCrewFileReader(socketPath string) *ipcCrewFileReader {
	return &ipcCrewFileReader{
		base: "http://crewshipd",
		client: &http.Client{
			Timeout: pipelineFilesBudget,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

func (r *ipcCrewFileReader) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path, nil)
	if err != nil {
		return nil, err
	}
	return r.client.Do(req)
}

func (r *ipcCrewFileReader) ListShared(ctx context.Context, crewID string) ([]provider.FileInfo, error) {
	resp, err := r.get(ctx, fmt.Sprintf("/crews/%s/files?recursive=true&subdir=shared", url.PathEscape(crewID)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("crew files list: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Files []provider.FileInfo `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("crew files list: decode: %w", err)
	}
	return body.Files, nil
}

func (r *ipcCrewFileReader) ReadShared(ctx context.Context, crewID, relPath string, limit int64) ([]byte, error) {
	resp, err := r.get(ctx, fmt.Sprintf("/crews/%s/files/download?path=%s", url.PathEscape(crewID), url.QueryEscape("shared/"+relPath)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("crew file read: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// sharedRelativeOf maps a listing path ("crews/<id>/shared/scripts/x.py",
// whatever the storage root spelled before it) to the share-relative form
// DescribeFiles reports. "" when the entry is not under the share.
func sharedRelativeOf(listingPath string) string {
	p := strings.ReplaceAll(listingPath, "\\", "/")
	if i := strings.Index(p, "/shared/"); i >= 0 {
		return p[i+len("/shared/"):]
	}
	return strings.TrimPrefix(strings.TrimPrefix(p, "shared/"), "/")
}

// enrichPipelineFiles fills presence, size, timestamp and description from
// the author crew's share. Best-effort by contract: any failure leaves the
// rows as DescribeFiles produced them (present:false, no size/time) — a
// file, a container or the sidecar being unavailable is information for the
// Files card, never a reason to fail the routine detail.
func (h *PipelineHandler) enrichPipelineFiles(ctx context.Context, authorCrewID string, files []pipeline.FileRef) []pipeline.FileRef {
	if len(files) == 0 || h.crewFiles == nil || strings.TrimSpace(authorCrewID) == "" {
		return files
	}
	deadline := time.Now().Add(pipelineFilesBudget)
	listCtx, cancelList := context.WithTimeout(ctx, pipelineFilesListTimeout)
	entries, err := h.crewFiles.ListShared(listCtx, authorCrewID)
	cancelList()
	if err != nil {
		h.logger.Debug("routine files: list shared volume", "crew_id", authorCrewID, "error", err)
		return files
	}
	present := make(map[string]provider.FileInfo, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if rel := sharedRelativeOf(e.Path); rel != "" {
			present[rel] = e
		}
	}
	for i := range files {
		if i >= pipelineFilesMaxIO {
			break
		}
		info, ok := present[files[i].Path]
		if !ok {
			continue
		}
		size := info.Size
		files[i].Present = true
		files[i].SizeBytes = &size
		if !info.ModTime.IsZero() {
			files[i].UpdatedAt = info.ModTime.UTC().Format(time.RFC3339)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			continue
		}
		readCtx, cancelRead := context.WithTimeout(ctx, pipelineFilesReadTimeout)
		head, rerr := h.crewFiles.ReadShared(readCtx, authorCrewID, files[i].Path, pipelineFilesHeaderBytes)
		cancelRead()
		if rerr != nil {
			h.logger.Debug("routine files: read header", "crew_id", authorCrewID, "path", files[i].Path, "error", rerr)
			continue
		}
		files[i].Description = pipeline.FileDescription(head)
	}
	return files
}
