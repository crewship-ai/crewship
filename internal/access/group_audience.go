package access

import (
	"context"
	"encoding/json"
)

// contextAudience identifies deliberate sharing using the exact current member
// revisions. A later permission regrant cannot revive a previous group epoch.
func contextAudience(ctx context.Context, q queryer, a Attempt) (string, error) {
	var visibility, raw string
	if err := q.QueryRowContext(ctx, `SELECT visibility FROM chats WHERE id=? AND workspace_id=?`, a.Chat, a.Workspace).Scan(&visibility); err != nil {
		return "", ErrDenied
	}
	if visibility == "private" {
		return "", nil
	}
	if visibility != "group" || a.AdmissionOperation == "run" {
		return "", ErrDenied
	}
	if err := q.QueryRowContext(ctx, `SELECT json_group_array(user_id) FROM (SELECT created_by AS user_id FROM chats WHERE id=? UNION SELECT user_id FROM chat_participants WHERE chat_id=? ORDER BY user_id)`, a.Chat, a.Chat).Scan(&raw); err != nil {
		return "", ErrDenied
	}
	var users []string
	if json.Unmarshal([]byte(raw), &users) != nil || len(users) < 2 || len(users) > 64 {
		return "", ErrDenied
	}
	type participant struct {
		User, Member string
		Revision     int64
	}
	vector := make([]participant, 0, len(users))
	included := false
	for _, user := range users {
		m, err := check(ctx, q, user, a.Workspace, Right{"agent", a.Agent, "chat"})
		if err != nil || m.Mode != "restricted" {
			return "", ErrDenied
		}
		included = included || user == a.Principal
		vector = append(vector, participant{user, m.ID, m.Revision})
	}
	if !included {
		return "", ErrDenied
	}
	data, err := json.Marshal(vector)
	if err != nil {
		return "", err
	}
	return digest(string(data)), nil
}
