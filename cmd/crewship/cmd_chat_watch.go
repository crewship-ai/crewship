package main

// Live refresh for the chat card lists and for room messages.
//
// The web chat keeps its Issue cards, decisions and proposals current by
// listening on `chat-hints:<chat-id>` — an invalidation channel that carries
// no data, only "re-read now" (internal/api/chat_entity_invalidations.go) —
// and re-reading through the ordinary authorised HTTP route. `--watch` does
// exactly that: print once, then re-print whenever a hint arrives.
//
// Rooms are the same shape on a different channel: a new room message is
// announced to each member on their own `user:<user-id>` channel as
// `conversation.updated` with the room id (internal/groupchatnotify), and
// `chat room messages --follow` reads what came after the last sequence it
// printed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// errWatchRefused marks the server's refusal of a subscription — an
// authorization answer, which reconnecting cannot change.
var errWatchRefused = errors.New("server refused")

// chatWatchDebounce coalesces a burst of hints (one operation commonly emits
// two) into one re-read. A var so tests can shorten it.
var chatWatchDebounce = 300 * time.Millisecond

// watchChannel subscribes to channel and calls onEvent for every frame on it
// until the context ends. A dropped socket reconnects with backoff and then
// calls onReconnect, because anything announced while disconnected is lost.
// onReconnect runs once the new subscription is in place, so nothing announced
// after the catch-up read can be missed.
func watchChannel(ctx context.Context, client *cli.Client, channel string, onEvent func(*cli.WSMessage), onReconnect func()) error {
	return watchChannelSubscribed(ctx, client, channel, onEvent, func(first bool) {
		if !first && onReconnect != nil {
			onReconnect()
		}
	})
}

// watchChannelSubscribed is watchChannel with a hook that runs after every
// successful subscribe (first reports the initial connection). Reading state
// from that hook, not before connecting, closes the gap in which an
// announcement has no subscriber.
func watchChannelSubscribed(ctx context.Context, client *cli.Client, channel string, onEvent func(*cli.WSMessage), onSubscribed func(first bool)) error {
	backoff := time.Second
	first := true
	for {
		isFirst := first
		first = false
		err := watchChannelOnce(ctx, client, channel, onEvent, func() {
			if onSubscribed != nil {
				onSubscribed(isFirst)
			}
		})
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errWatchRefused) || isPermanentStreamError(err) {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s[reconnecting in %s: %v]%s\n", cli.Dim, backoff, err, cli.Reset)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func watchChannelOnce(ctx context.Context, client *cli.Client, channel string, onEvent func(*cli.WSMessage), onSubscribed func()) error {
	token, err := cli.WSTokenFromServer(client)
	if err != nil {
		return fmt.Errorf("get WS token: %w", err)
	}
	ws, err := cli.NewWSClient(streamServerURL(), token)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
	defer stop()
	defer ws.Close()
	if err := ws.Subscribe(channel); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	// Subscribe has no acknowledgement. The server handles frames in order, so
	// the pong for a ping sent right behind it proves the subscription is
	// registered; only then may the caller read state, or a message committed
	// in between would have no subscriber to announce it to.
	if err := ws.Ping(); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	for registered := false; !registered; {
		msg, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		if reason, ok := cli.CloseReason(msg); ok {
			return cli.WithExitCode(fmt.Errorf("%w %s: %s", errWatchRefused, channel, reason), cli.ExitAuth)
		}
		if msg.Type == "pong" {
			registered = true
		} else if msg.Channel == channel {
			onEvent(msg)
		}
	}
	onSubscribed()
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		if reason, ok := cli.CloseReason(msg); ok {
			// "access denied" on subscribe is the authorizer's answer and does
			// not heal by reconnecting.
			return cli.WithExitCode(fmt.Errorf("%w %s: %s", errWatchRefused, channel, reason), cli.ExitAuth)
		}
		if msg.Channel == channel {
			onEvent(msg)
		}
	}
}

// watchCommandContext ends on SIGINT/SIGTERM. Watching ends only that way, and
// it is a normal end, so it exits 0.
func watchCommandContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// addChatHintsWatch gives a chat card list a --watch flag. The wrapped command
// renders exactly as without it; with it the render repeats after each hint.
func addChatHintsWatch(cmd *cobra.Command) {
	cmd.Flags().Bool("watch", false, "Keep running and print the list again whenever it changes (Ctrl-C to stop)")
	render := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		watch, _ := c.Flags().GetBool("watch")
		if !watch {
			return render(c, args)
		}
		if err := render(c, args); err != nil {
			return err
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		ctx, cancel := watchCommandContext(c)
		defer cancel()
		pending := make(chan struct{}, 1)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-pending:
				}
				// Debounce: let the rest of a burst arrive, then drain it.
				select {
				case <-time.After(chatWatchDebounce):
				case <-ctx.Done():
					return
				}
				select {
				case <-pending:
				default:
				}
				fmt.Fprintf(os.Stderr, "%s[updated %s]%s\n", cli.Dim, time.Now().Format("15:04:05"), cli.Reset)
				if err := render(c, args); err != nil {
					fmt.Fprintf(os.Stderr, "%s[refresh failed]%s %v\n", cli.Yellow, cli.Reset, err)
				}
			}
		}()
		poke := func() {
			select {
			case pending <- struct{}{}:
			default:
			}
		}
		return watchChannel(ctx, client, "chat-hints:"+args[0], func(*cli.WSMessage) { poke() }, poke)
	}
}

// roomMessage is the part of groupchat.Message a follower prints.
type roomMessage struct {
	ID           string `json:"id" yaml:"id"`
	Sequence     int64  `json:"sequence" yaml:"sequence"`
	AuthorName   string `json:"author_name" yaml:"author_name"`
	AuthorUserID string `json:"author_user_id" yaml:"author_user_id"`
	Kind         string `json:"kind" yaml:"kind"`
	Content      string `json:"content" yaml:"content"`
	CreatedAt    string `json:"created_at" yaml:"created_at"`
}

// followRoomMessages prints a room's messages after `after` and then every new
// one as it lands. Table output is one line per message; json/ndjson print each
// message as the server sent it, one object per line.
func followRoomMessages(cmd *cobra.Command, roomID string, after int64, fromStart bool, limit int) error {
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return err
	}
	var me struct {
		UserID string `json:"user_id" yaml:"user_id"`
	}
	if err := getJSON(client, "/api/v1/auth/cli-token/validate", &me); err != nil {
		return err
	}
	if me.UserID == "" {
		return fmt.Errorf("could not determine your user id for the notification channel")
	}
	format := cli.ResolveFormat(flagFormat, cliCfg)
	machine := format == "json" || format == "ndjson"

	last := after
	// Without --after-sequence, start where `chat room messages` would: the
	// latest page, not the room's whole history.
	latest := !fromStart
	fetch := func() error {
		for {
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			if !latest {
				q.Set("after_sequence", strconv.FormatInt(last, 10))
			}
			resp, err := client.Do(http.MethodGet, chatRoomPath(roomID)+"/messages?"+q.Encode(), nil)
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var page struct {
				Messages []json.RawMessage `json:"messages" yaml:"messages"`
				HasMore  bool              `json:"has_more" yaml:"has_more"`
			}
			if err := cli.ReadJSON(resp, &page); err != nil {
				return err
			}
			wasLatest := latest
			latest = false
			for _, raw := range page.Messages {
				var m roomMessage
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				if m.Sequence <= last && !wasLatest {
					continue
				}
				last = m.Sequence
				if machine {
					fmt.Println(string(raw))
					continue
				}
				who := m.AuthorName
				if who == "" {
					who = m.AuthorUserID
				}
				fmt.Printf("%s#%d%s %s%s%s: %s\n", cli.Dim, m.Sequence, cli.Reset, cli.Bold, sanitizeTerminal(who), cli.Reset, sanitizeTerminal(m.Content))
			}
			// The latest page's has_more points at OLDER history; only a
			// catch-up page's has_more means newer messages are still waiting.
			if wasLatest || !page.HasMore || len(page.Messages) == 0 {
				return nil
			}
		}
	}
	ctx, cancel := watchCommandContext(cmd)
	defer cancel()
	refresh := func() {
		if err := fetch(); err != nil {
			fmt.Fprintf(os.Stderr, "%s[refresh failed]%s %v\n", cli.Yellow, cli.Reset, err)
		}
	}
	// The first read happens after the subscription is in place: a message
	// committed between a read and the subscribe would have no subscriber and
	// never be printed.
	var firstErr error
	err = watchChannelSubscribed(ctx, client, "user:"+me.UserID, func(msg *cli.WSMessage) {
		if msg.Type != "conversation.updated" {
			return
		}
		var p struct {
			ConversationID string `json:"conversation_id" yaml:"conversation_id"`
		}
		if json.Unmarshal(msg.Payload, &p) == nil && p.ConversationID == roomID {
			refresh()
		}
	}, func(first bool) {
		if first {
			// A failed first read is fatal, as before the reorder.
			if firstErr = fetch(); firstErr != nil {
				cancel()
			}
			return
		}
		refresh()
	})
	if firstErr != nil {
		return firstErr
	}
	return err
}
