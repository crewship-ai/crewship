package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Discussion cursors select a continuation, never authority. Every handler must
// authorize its Page against live state before parsing or using this value.
type discussionPage struct {
	Workspace string `json:"workspace"`
	Page      string `json:"page"`
	Kind      string `json:"kind"`
	Revision  int64  `json:"revision"`
	Status    string `json:"status"`
	Created   string `json:"created"`
	ID        string `json:"id"`
	Open      int    `json:"open"`
	Limit     int    `json:"-"`
}

func parseDiscussionPage(r *http.Request, workspace, page, kind string, revision int64) (discussionPage, error) {
	p := discussionPage{Workspace: workspace, Page: page, Kind: kind, Revision: revision, Status: r.URL.Query().Get("status"), Limit: 100}
	if p.Status == "" {
		p.Status = "all"
	}
	if p.Status != "all" && p.Status != "open" && p.Status != "resolved" {
		return p, errors.New("status must be all, open or resolved")
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return p, errors.New("limit must be between 1 and 100")
		}
		p.Limit = n
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 4096 {
			return p, errors.New("invalid discussion cursor")
		}
		data, err := base64.RawURLEncoding.DecodeString(raw)
		var c discussionPage
		if err != nil || json.Unmarshal(data, &c) != nil || c.Workspace != workspace || c.Page != page || c.Kind != kind || c.Revision != revision || c.Status != p.Status || c.Created == "" || c.ID == "" || (c.Open != 0 && c.Open != 1) {
			return p, errors.New("invalid discussion cursor")
		}
		p.Created, p.ID, p.Open = c.Created, c.ID, c.Open
	}
	return p, nil
}

func (p discussionPage) cursor(created, id string, open int) string {
	p.Created, p.ID, p.Open = created, id, open
	data, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(data)
}
