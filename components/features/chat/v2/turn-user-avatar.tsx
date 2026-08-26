"use client"

import { UserAvatar } from "@/components/ui/user-avatar"
import { useSession } from "@/hooks/use-auth"

/**
 * Whoever sent this turn, in the transcript's right gutter.
 *
 * Today every user turn in a chat is the signed-in person's, so this reads
 * the session and draws them. The `authorName` argument is the seam for what
 * comes next: `ChatTurn` already carries an `authorUserId` for group chats,
 * and classic already resolves it to a name — it just has nowhere to put a
 * face. When the roster lookup lands, this is the one component that changes,
 * and the layout around it does not, because the gutter is already the right
 * width and already on the right side of the bubble.
 *
 * A gutter rather than a name above the bubble: names stack vertically and
 * cost a line each, faces stack horizontally and cost nothing. In a thread
 * with three people that is the difference between skimming and reading.
 */
export function ChatTurnUserAvatar({ authorName }: { authorName?: string | null }) {
  const { data: session } = useSession()
  const user = session?.user

  // A teammate's turn: we have a display name from the group-chat resolver
  // but no account record, so initials are the honest render — better than
  // showing the reader their OWN face on somebody else's message.
  if (authorName) {
    return (
      <UserAvatar
        name={authorName}
        email={authorName}
        className="h-8 w-8"
        textClassName="text-[10px]"
      />
    )
  }

  if (!user) {
    return <div className="h-8 w-8" aria-hidden="true" />
  }

  return (
    <UserAvatar
      name={user.name}
      email={user.email}
      src={user.avatar_url || null}
      className="h-8 w-8"
      textClassName="text-[10px]"
    />
  )
}
