package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// TemplateStore reads and writes the per-category message templates.
type TemplateStore struct {
	db *sql.DB
}

// NewTemplateStore wires a store over the control-plane database.
func NewTemplateStore(db *sql.DB) *TemplateStore { return &TemplateStore{db: db} }

// List returns every template in the workspace, ordered so the output is
// stable to read and to diff.
func (s *TemplateStore) List(ctx context.Context, workspaceID string) ([]MessageTemplate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT category, COALESCE(channel_id,''), title_template, body_template
		   FROM notification_templates
		  WHERE workspace_id = ?
		  ORDER BY category, COALESCE(channel_id,'')`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list notification templates: %w", err)
	}
	defer rows.Close()

	var out []MessageTemplate
	for rows.Next() {
		var t MessageTemplate
		if err := rows.Scan(&t.Category, &t.ChannelID, &t.Title, &t.Body); err != nil {
			return nil, fmt.Errorf("scan notification template: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read notification templates: %w", err)
	}
	return out, nil
}

// Upsert stores one template, replacing any at the same (category, channel).
//
// A template whose title and body are BOTH empty is a deletion rather than a
// row saying "change nothing": leaving it would mean the list reports a
// template where none applies, and an operator clearing both fields in a form
// means "stop overriding this".
func (s *TemplateStore) Upsert(ctx context.Context, workspaceID string, t MessageTemplate) error {
	if err := ValidateTemplate(t); err != nil {
		return err
	}
	if strings.TrimSpace(t.Title) == "" && strings.TrimSpace(t.Body) == "" {
		return s.Delete(ctx, workspaceID, t.Category, t.ChannelID)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notification_templates (id, workspace_id, category, channel_id, title_template, body_template)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(workspace_id, category, COALESCE(channel_id,''))
		 DO UPDATE SET title_template = excluded.title_template,
		               body_template  = excluded.body_template,
		               updated_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		newTemplateID(), workspaceID, t.Category, nullableChannel(t.ChannelID), t.Title, t.Body)
	if err != nil {
		return fmt.Errorf("save notification template for %q: %w", t.Category, err)
	}
	return nil
}

// Delete removes the template at (category, channel). Removing one that is not
// there is not an error — the caller asked for a state, and it holds.
func (s *TemplateStore) Delete(ctx context.Context, workspaceID, category, channelID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM notification_templates
		  WHERE workspace_id = ? AND category = ? AND COALESCE(channel_id,'') = ?`,
		workspaceID, category, channelID)
	if err != nil {
		return fmt.Errorf("delete notification template for %q: %w", category, err)
	}
	return nil
}

// Resolve returns the template that applies to a category on a channel, or a
// zero template when none does.
//
// A channel-specific row wins over the all-channels one. Anything else would
// make the narrower row pointless — an operator writes one precisely because
// that destination should differ.
func (s *TemplateStore) Resolve(ctx context.Context, workspaceID, category, channelID string) (MessageTemplate, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT category, COALESCE(channel_id,''), title_template, body_template
		   FROM notification_templates
		  WHERE workspace_id = ? AND category = ? AND COALESCE(channel_id,'') IN (?, '')
		  ORDER BY COALESCE(channel_id,'') DESC
		  LIMIT 1`, workspaceID, category, channelID)

	var t MessageTemplate
	switch err := row.Scan(&t.Category, &t.ChannelID, &t.Title, &t.Body); {
	case err == sql.ErrNoRows:
		return MessageTemplate{}, nil
	case err != nil:
		return MessageTemplate{}, fmt.Errorf("resolve notification template for %q: %w", category, err)
	}
	return t, nil
}

// ValidateTemplate rejects a template that could never apply.
//
// Checked at the write, not at delivery: a category nobody can produce routes
// to nobody, and finding that out when the notification fails to arrive is the
// expensive way to learn it.
func ValidateTemplate(t MessageTemplate) error {
	if !ValidCategory(t.Category) {
		return fmt.Errorf("notify: %q is not a notification category", t.Category)
	}
	for field, tmpl := range map[string]string{"title": t.Title, "body": t.Body} {
		for _, ref := range templateRefsIn(tmpl) {
			if !templateRefIsKnown(ref) {
				return fmt.Errorf(
					"notify: %s template references %q, which is neither vars.<fact> nor source.<title|body|category|kind>",
					field, ref)
			}
		}
	}
	return nil
}

// templateRefsIn lists the `{{ ... }}` bodies in a template.
func templateRefsIn(tmpl string) []string {
	matches := messageTemplateRE.FindAllStringSubmatch(tmpl, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// templateRefIsKnown reports whether a reference names a namespace that
// exists. The KEY under vars is deliberately not checked — which facts an
// event carries depends on the event, and refusing a template because this
// week's sample lacks a field would be worse than rendering it empty.
func templateRefIsKnown(ref string) bool {
	head, rest, found := strings.Cut(ref, ".")
	if !found || rest == "" {
		return false
	}
	switch head {
	case "vars":
		return true
	case "source":
		switch rest {
		case "title", "body", "category", "kind":
			return true
		}
	}
	return false
}

// newTemplateID mints a row id in the same shape as generateChannelID. The id
// is bookkeeping only — a template is addressed by (workspace, category,
// channel), which is what the unique index enforces and what every caller
// knows.
func newTemplateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "ntpl_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return "ntpl_" + hex.EncodeToString(b)
}

func nullableChannel(id string) any {
	if id == "" {
		return nil
	}
	return id
}
