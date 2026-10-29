package main

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const craftNativeJSONLimit = 45 << 20
const craftNativeBytesLimit = 32 << 20

type craftDocumentSnapshot struct {
	WorkspaceID    string `json:"workspaceId" yaml:"workspaceId"`
	PageID         string `json:"pageId" yaml:"pageId"`
	ApplicationID  string `json:"applicationId" yaml:"applicationId"`
	EnginePin      string `json:"enginePin" yaml:"enginePin"`
	SessionID      string `json:"sessionId" yaml:"sessionId"`
	Generation     string `json:"generation" yaml:"generation"`
	BaseVersion    string `json:"baseVersion" yaml:"baseVersion"`
	EditorRevision string `json:"editorRevision,omitempty" yaml:"editorRevision,omitempty"`
	ContentHash    string `json:"contentHash,omitempty" yaml:"contentHash,omitempty"`
	Bytes          []byte `json:"bytes,omitempty" yaml:"bytes,omitempty"`
	TargetVersion  string `json:"targetVersion,omitempty" yaml:"targetVersion,omitempty"`
}

func craftKnownNativeApplication(application string) bool {
	return application == "wordcraft" || application == "gridcraft" || application == "deckcraft"
}
func craftPositiveVersion(raw string) bool {
	n, e := strconv.ParseInt(raw, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == raw
}
func craftDocumentDigest(raw string) bool {
	if len(raw) != 64 || strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}
func craftDocumentMutation(action string) bool {
	return action == "share" || action == "save" || action == "import" || action == "restore" || action == "reconcile"
}
func craftValidateDocumentAction(a craftAction, session string) error {
	s := a.Snapshot
	if s == nil || s.SessionID != session || !craftPositiveVersion(s.Generation) || !craftPositiveVersion(s.BaseVersion) || !craftDocumentDigest(s.EnginePin) || !craftKnownNativeApplication(s.ApplicationID) || len(s.Bytes) > craftNativeBytesLimit {
		return fmt.Errorf("invalid native snapshot binding")
	}
	for _, id := range []string{s.WorkspaceID, s.PageID, s.SessionID} {
		if id == "" || len(id) > 256 || strings.ContainsRune(id, 0) {
			return fmt.Errorf("invalid native snapshot binding")
		}
	}
	if a.Action == "share" {
		if !craftPositiveVersion(s.TargetVersion) || len(s.Bytes) != 0 || s.EditorRevision != "" || s.ContentHash != "" {
			return fmt.Errorf("share requires an existing version selection without snapshot content")
		}
		return nil
	}
	if a.Action == "restore" {
		if !craftPositiveVersion(s.TargetVersion) {
			return fmt.Errorf("invalid native restore target")
		}
		return nil
	}
	if s.EditorRevision == "" || len(s.EditorRevision) > 256 || strings.ContainsRune(s.EditorRevision, 0) || !strings.HasPrefix(s.ContentHash, "sha256:") || !craftDocumentDigest(strings.TrimPrefix(s.ContentHash, "sha256:")) {
		return fmt.Errorf("invalid native snapshot binding")
	}
	if (a.Action == "save" || a.Action == "import") && len(s.Bytes) == 0 {
		return fmt.Errorf("native snapshot bytes required")
	}
	return nil
}

// The ordinary preview command has no editor admission or lease. Missing shared
// content is an error, never a fallback to the private latest document.
func craftPreview(cmd *cobra.Command, args []string) error {
	client, err := pageClient()
	if err != nil {
		return err
	}
	client = client.WithContext(cmd.Context())
	// An HTTP redirect cannot replace the explicit shared selector with latest.
	httpClient := *client.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.HTTPClient = &httpClient
	response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/preview")
	return craftResponseLimit(cmd, response, err, "", craftNativeJSONLimit)
}

// Only existing-version selection fields travel to the share endpoint. The
// private X-Craft-Session remains bound to this connect process's client.
type craftShareSelection struct {
	WorkspaceID   string `json:"workspaceId" yaml:"workspaceId"`
	PageID        string `json:"pageId" yaml:"pageId"`
	ApplicationID string `json:"applicationId" yaml:"applicationId"`
	EnginePin     string `json:"enginePin" yaml:"enginePin"`
	SessionID     string `json:"sessionId" yaml:"sessionId"`
	Generation    string `json:"generation" yaml:"generation"`
	BaseVersion   string `json:"baseVersion" yaml:"baseVersion"`
	TargetVersion string `json:"targetVersion" yaml:"targetVersion"`
}
