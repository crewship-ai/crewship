package server

import (
	"context"

	"github.com/crewship-ai/crewship/internal/groupchatnotify"
)

// Keep the durable projection worker on the server run context and join it
// through the existing background wait group before teardown closes resources.
func (s *Server) startConversationNotifications(ctx context.Context) {
	if s.db == nil {
		return
	}
	dispatcher := groupchatnotify.New(s.db, s.wsHub, s.logger)
	s.bgWg.Add(1)
	go func() { defer s.bgWg.Done(); dispatcher.Run(ctx) }()
}
