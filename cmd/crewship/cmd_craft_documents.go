package main

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
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
	return action == "save" || action == "import" || action == "restore" || action == "reconcile"
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
