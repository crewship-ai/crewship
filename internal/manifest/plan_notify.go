package manifest

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Planning for notification channels, their agent grants, and the Composio
// key.
//
// The rule that shapes all of it: a secret is only ever read from the run's
// CredentialSource, never from the manifest. That means a channel can be
// UNPLANNABLE — the file declares it, but this run has no value for it — and
// the honest response is to skip it and say so. Creating it anyway would put
// an enabled row in the workspace that looks configured and delivers nowhere,
// which is worse than an absent one because nobody goes looking for it.

// planNotifications appends the workspace's channel, grant and Composio items.
func (pb *planBuilder) planNotifications(ctx context.Context, ws *WorkspaceDocument) error {
	if ws.Spec.Composio != nil && ws.Spec.Composio.APIKeyFromEnv != "" {
		pb.planComposioKey(ws.Spec.Composio.APIKeyFromEnv)
	}

	if len(ws.Spec.NotificationChannels) == 0 {
		return nil
	}

	remote, err := pb.client.ListNotificationChannels(ctx)
	if err != nil {
		// A workspace that has never had a channel can 404 or answer with an
		// empty body depending on how the route is wired; neither is a reason
		// to abort a plan that is mostly about crews and agents.
		remote = nil
	}

	for i := range ws.Spec.NotificationChannels {
		pb.planNotificationChannel(&ws.Spec.NotificationChannels[i], remote)
	}
	return nil
}

// planComposioKey sets the workspace's Composio project key.
func (pb *planBuilder) planComposioKey(envVar string) {
	value, ok := pb.opts.Secrets.ValueFor(envVar)
	if !ok {
		pb.plan.Warnings = append(pb.plan.Warnings, fmt.Sprintf(
			"composio: %s is not set, so the API key was left alone — pass --from-env or --secrets-file to configure managed tools",
			envVar))
		return
	}
	pb.appendItem(ActionUpdate, "composio", "api-key (from "+envVar+")",
		func(ctx context.Context, c *Client, _ Options) error {
			return c.SetComposioAPIKey(ctx, value)
		})
}

// planNotificationChannel plans one channel plus its agent grants.
func (pb *planBuilder) planNotificationChannel(ch *NotificationChannel, remote []NotifyChannelResponse) {
	body, missing := pb.notificationBody(ch)
	if len(missing) > 0 {
		// Reported rather than silently dropped: an operator who ran without
		// --from-env needs to know which variables would have been read, and
		// the plan is where they are looking.
		sort.Strings(missing)
		pb.plan.Skipped = append(pb.plan.Skipped, fmt.Sprintf(
			"notification channel %s (needs %s — pass --from-env or --secrets-file)",
			ch.Slug, strings.Join(missing, ", ")))
		return
	}

	if existing := matchRemoteChannel(ch, remote); existing != nil {
		// Identity is (type, provider, destination) — the server has no slug
		// column for channels — so a match means "this channel is already
		// here". Only the parts a manifest owns are patched; the destination
		// is not, because re-sending a secret that already works is churn
		// with a blast radius.
		id := existing.ID
		patch := map[string]any{}
		if len(ch.Categories) > 0 && !sameStrings(ch.Categories, existing.Cats) {
			patch["categories"] = ch.Categories
		}
		if ch.Enabled != nil && *ch.Enabled != existing.Enabled {
			patch["enabled"] = *ch.Enabled
		}
		if len(patch) == 0 {
			pb.appendItem(ActionUnchanged, "notification_channel", ch.Slug, nil)
		} else {
			pb.appendItem(ActionUpdate, "notification_channel", ch.Slug,
				func(ctx context.Context, c *Client, _ Options) error {
					return c.PatchNotificationChannel(ctx, id, patch)
				})
		}
		pb.planChannelGrants(ch, func(context.Context, *Client) (string, error) { return id, nil })
		pb.planChannelPrefs(ch, func(context.Context, *Client) (string, error) { return id, nil })
		return
	}

	// Create. The id is only known at exec time, so the grants that follow
	// read it from this cell rather than from a value captured at plan time.
	created := new(string)
	pb.appendItem(ActionCreate, "notification_channel", ch.Slug,
		func(ctx context.Context, c *Client, _ Options) error {
			out, err := c.CreateNotificationChannel(ctx, body)
			if err != nil {
				return err
			}
			*created = out.ID
			return nil
		})
	channelID := func(context.Context, *Client) (string, error) {
		if *created == "" {
			return "", fmt.Errorf("channel %q was not created, so its grants cannot be applied", ch.Slug)
		}
		return *created, nil
	}
	pb.planChannelGrants(ch, channelID)
	pb.planChannelPrefs(ch, channelID)
}

// planChannelPrefs routes categories to this channel for the applying user.
//
// Its own item, like the grants: a channel that exists but delivers nothing is
// the failure mode this exists to prevent, and folding it into the create
// would hide which half went wrong.
func (pb *planBuilder) planChannelPrefs(ch *NotificationChannel, channelID func(context.Context, *Client) (string, error)) {
	if len(ch.DeliverToMe) == 0 {
		return
	}
	cats := append([]string(nil), ch.DeliverToMe...)
	pb.appendItem(ActionCreate, "notification_pref", ch.Slug+" → me",
		func(ctx context.Context, c *Client, _ Options) error {
			id, err := channelID(ctx, c)
			if err != nil {
				return err
			}
			return c.RouteCategoriesToChannel(ctx, id, cats)
		})
}

// planChannelGrants appends one item per agent allowed to post to the channel.
//
// Separate items on purpose: a pairing is gated on the CHANNEL's own authority
// and can be refused independently, so folding it into the create would make
// that refusal read as the channel having failed.
func (pb *planBuilder) planChannelGrants(ch *NotificationChannel, channelID func(context.Context, *Client) (string, error)) {
	for _, agentSlug := range ch.Agents {
		slug := agentSlug
		pb.appendItem(ActionCreate, "notification_grant", ch.Slug+":"+slug,
			func(ctx context.Context, c *Client, _ Options) error {
				id, err := channelID(ctx, c)
				if err != nil {
					return err
				}
				agentID, err := c.FindAgentIDBySlug(ctx, slug)
				if err != nil {
					return fmt.Errorf("grant %s to %s: %w", ch.Slug, slug, err)
				}
				return c.AllowAgentOnChannel(ctx, id, agentID)
			})
	}
}

// notificationBody builds the create body, reading every secret from the run's
// source. The second return names the environment variables that had no value,
// which is what makes the channel unplannable.
func (pb *planBuilder) notificationBody(ch *NotificationChannel) (map[string]any, []string) {
	body := map[string]any{}
	var missing []string

	switch ch.Type {
	case "email":
		body["type"] = "email"
		body["to"] = ch.To
	case "webhook":
		body["type"] = "webhook"
		url, ok := pb.opts.Secrets.ValueFor(ch.URLFromEnv)
		if !ok {
			missing = append(missing, ch.URLFromEnv)
		}
		body["url"] = url
	case "chat":
		// "shoutrrr" is the value the server stores for every chat and push
		// destination — the delivery library's own name, which predates the
		// provider catalogue. The manifest says "chat"; the wire says this.
		body["type"] = "shoutrrr"
		body["provider"] = ch.Provider
		fields := map[string]string{}
		for field, envVar := range ch.FieldsFromEnv {
			v, ok := pb.opts.Secrets.ValueFor(envVar)
			if !ok {
				missing = append(missing, envVar)
				continue
			}
			fields[field] = v
		}
		body["fields"] = fields
	}

	if len(ch.Categories) > 0 {
		body["categories"] = ch.Categories
	}
	if ch.MinPriority != "" && ch.MinPriority != "low" {
		body["min_priority"] = ch.MinPriority
	}
	return body, missing
}

// matchRemoteChannel finds the existing row this declaration refers to.
//
// Channels have no slug server-side, so identity is what a person would use:
// the transport plus where it points. For chat that is (type, provider) —
// the delivery URL is never returned by the list endpoint, so it cannot take
// part, which is why two Discord channels in one workspace are not
// distinguishable from a manifest today. That limit is deliberate rather than
// hidden: a second one is a second slug the server cannot tell apart, and
// guessing would be worse than declining to.
func matchRemoteChannel(ch *NotificationChannel, remote []NotifyChannelResponse) *NotifyChannelResponse {
	for i := range remote {
		r := &remote[i]
		if r.Scope == "user" {
			continue // personal channels belong to their owner, not the manifest
		}
		switch ch.Type {
		case "email":
			if r.Type == "email" && r.To == ch.To {
				return r
			}
		case "webhook":
			// The URL is a secret the manifest does not hold, so a webhook is
			// matched on transport alone. One declared webhook per workspace.
			if r.Type == "webhook" {
				return r
			}
		case "chat":
			if r.Type == "shoutrrr" && r.Provider == ch.Provider {
				return r
			}
		}
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// planComposioGrants appends one item per toolkit an agent is granted.
//
// A grant for a toolkit nobody has connected is legitimate and must not fail
// the apply: the account behind it is a per-user OAuth handshake no manifest
// can perform, and the grant takes effect the moment someone connects. The
// refusal is downgraded to a warning so a demo manifest lands cleanly on an
// instance where the browser step has not happened yet.
func (pb *planBuilder) planComposioGrants(agentSlug string, grants []ComposioToolkitGrant) {
	if len(grants) == 0 {
		return
	}
	// What the agent already has, when it already exists. A new agent has no
	// id yet and therefore no grants, so an empty set is the right answer for
	// it too — no special case needed.
	var existing []ComposioBinding
	if agentID, err := pb.client.FindAgentIDBySlug(context.Background(), agentSlug); err == nil {
		existing = pb.client.ListAgentToolkits(context.Background(), agentID)
	}

	for i := range grants {
		g := grants[i]
		mode := g.Mode
		if mode == "" {
			mode = "read"
		}
		// Re-binding a grant that already matches is churn: the plan would
		// never settle, and "apply until it says nothing changed" is the
		// whole contract.
		if composioGrantSatisfied(existing, g.Toolkit, mode) {
			pb.appendItem(ActionUnchanged, "composio_grant", agentSlug+":"+g.Toolkit, nil)
			continue
		}
		// Decided HERE, not by trying. The bind endpoint requires a user_id,
		// so a grant without one cannot succeed — planning a create for it
		// meant the server refused, the refusal was downgraded to a warning
		// nobody saw (Exec-time warnings land on Apply's internal plan, not
		// the printed one), and the run reported "1 created" for a grant
		// that never existed. Every apply, forever.
		if g.UserID == "" {
			pb.plan.Skipped = append(pb.plan.Skipped, fmt.Sprintf(
				"composio grant %s → %s (no user_id — connect an account in Integrations → Tools, then set user_id on the grant)",
				agentSlug, g.Toolkit))
			continue
		}
		pb.appendItem(ActionCreate, "composio_grant", agentSlug+":"+g.Toolkit,
			func(ctx context.Context, c *Client, _ Options) error {
				agentID, err := c.FindAgentIDBySlug(ctx, agentSlug)
				if err != nil {
					return fmt.Errorf("composio grant for %s: %w", agentSlug, err)
				}
				body := map[string]any{"toolkit": g.Toolkit, "mode": mode}
				if len(g.Tools) > 0 {
					body["tools"] = g.Tools
				}
				if g.UserID != "" {
					body["user_id"] = g.UserID
				}
				if err := c.BindAgentToolkit(ctx, agentID, body); err != nil {
					if composioNeedsAccount(err) {
						pb.plan.Warnings = append(pb.plan.Warnings, fmt.Sprintf(
							"composio: %s is not granted %s yet — no connected account to act as. Connect one in Integrations → Tools, then set user_id on the grant and re-apply.",
							agentSlug, g.Toolkit))
						return nil
					}
					return err
				}
				return nil
			})
	}
}

// composioGrantSatisfied reports whether the agent already holds this grant at
// this scope. Mode is compared too: widening read to full is a real change.
func composioGrantSatisfied(existing []ComposioBinding, toolkit, mode string) bool {
	for _, b := range existing {
		if b.Toolkit == toolkit && b.Mode == mode {
			return true
		}
	}
	return false
}

// composioNeedsAccount reports whether the refusal was "nothing is connected
// yet" rather than a real failure. Matched on the message because the endpoint
// answers 400 for both, and a manifest that cannot tell them apart would
// either mask genuine errors or fail every fresh instance.
func composioNeedsAccount(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no connected account") ||
		strings.Contains(msg, "not connected") ||
		strings.Contains(msg, "connect an account") ||
		// "user_id is required" is the same condition wearing a different
		// message: the user id IS the connected account's identity, so a
		// manifest that omits it is describing a grant that cannot be made
		// until somebody connects one. A portable file cannot carry an
		// instance-specific id, so this must not fail the apply.
		strings.Contains(msg, "user_id is required")
}
