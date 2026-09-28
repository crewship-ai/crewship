/** Three dots in an agent bubble. Motion stops under reduced motion; the
 *  dots stay, so the state is still readable. */
export function TypingDots({ name }: { name?: string | null }) {
  const who = name?.trim() || "Agent"
  return (
    <div
      role="status"
      aria-label={`${who} is working`}
      className="inline-flex w-fit items-center gap-1 rounded-[18px] rounded-tl-[6px] border border-border bg-card px-4 py-3.5"
    >
      {[0, 1, 2].map((i) => (
        <span
          key={i}
          data-slot="typing-dot"
          aria-hidden="true"
          className="h-1.5 w-1.5 rounded-full bg-muted-foreground motion-safe:animate-bounce"
          style={{ animationDelay: `${i * 140}ms`, animationDuration: "1s" }}
        />
      ))}
    </div>
  )
}
