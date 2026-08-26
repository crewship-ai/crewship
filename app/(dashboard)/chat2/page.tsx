import { Chat2Client } from "./chat2-client"

/**
 * `/chat2` — the chat surface's proving ground.
 *
 * Everything here is real: real agents, real sessions, the same WebSocket and
 * the same ChatPanel `/chat` mounts. What differs is the shell around it and
 * the skin the transcript renders in. Keeping it as a second ROUTE rather than
 * a flag on the first one is deliberate — the two can be opened side by side
 * in two tabs against the same conversation, which is the only honest way to
 * judge whether the new one is actually better.
 *
 * When it wins, `/chat` adopts the skin and this route is deleted. It is not
 * meant to survive as a permanent second door.
 */
export default function Chat2Page() {
  return <Chat2Client />
}
