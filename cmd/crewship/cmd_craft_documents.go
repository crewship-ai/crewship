package main

import (
	"encoding/hex"
	"encoding/json"
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
	if s == nil || s.SessionID != session || !craftPositiveVersion(s.Generation) || !craftPositiveVersion(s.BaseVersion) || !craftDocumentDigest(s.EnginePin) || (s.ApplicationID != "wordcraft" && s.ApplicationID != "gridcraft") || len(s.Bytes) > craftNativeBytesLimit {
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

// craftNativeVersionsLimit bounds one history listing (the server returns at
// most 200 entries, metadata only).
const craftNativeVersionsLimit = 1 << 20

// craftVersions opens one process-local native admission, reads the version
// list under it and exits. It never acquires a lease and never writes; the
// private session token is never printed.
func craftVersions(cmd *cobra.Command, args []string) error {
	c, e := pageClient()
	if e != nil {
		return e
	}
	c = c.WithContext(cmd.Context())
	httpClient := *c.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.HTTPClient = &httpClient
	r, e := c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/sessions", struct{}{})
	if e != nil {
		return craftTransportError("native session request failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 201 {
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			return craftHTTPError(r, "")
		}
		return fmt.Errorf("native session HTTP %d", r.StatusCode)
	}
	raw, e := craftReadJSON(r.Body)
	if e != nil {
		return e
	}
	var admission struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	if e = json.Unmarshal(raw, &admission); e != nil || !craftDocumentDigest(admission.Token) || admission.ID == "" || admission.ID == admission.Token {
		return fmt.Errorf("invalid native session response")
	}
	response, e := c.WithHeader("X-Craft-Session", admission.Token).Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/document/versions")
	return craftResponseLimit(cmd, response, e, admission.Token, craftNativeVersionsLimit)
}
