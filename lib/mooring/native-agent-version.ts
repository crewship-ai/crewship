import { applyAgentVersion, type LiveApplyTargets } from "./native-live-apply"
import { NATIVE_EXECUTE_COMMANDS, NATIVE_EXECUTE_MAX_COMMANDS, type NativeExecuteCommand } from "./native-editor-port"
import type { RebaseOutcome } from "./native-rebase"

/** An agent's saved version of the open Sheet, as its tool result reports it. */
export interface AgentVersionEvent {
  pageId: string
  version: string
  contentHash?: string
  baseVersion?: string
  commands?: NativeExecuteCommand[]
  commandsOmitted?: boolean
}

const VERSION = /^[1-9][0-9]*$/
export const isNativeVersionText = (value: unknown): value is string => typeof value === "string" && value.length <= 20 && VERSION.test(value)

// --- A small bus: the chat card publishes, the editor host of that page listens. ---
type Listener = (event: AgentVersionEvent) => void
const listeners = new Map<string, Set<Listener>>()

export function subscribeAgentVersions(pageId: string, listener: Listener): () => void {
  let set = listeners.get(pageId)
  if (!set) listeners.set(pageId, set = new Set())
  set.add(listener)
  return () => { set!.delete(listener); if (set!.size === 0) listeners.delete(pageId) }
}

/** Nothing is buffered: an editor that opens later reads the latest version anyway. */
export function publishAgentVersion(event: AgentVersionEvent): void {
  for (const listener of [...(listeners.get(event.pageId) ?? [])]) listener(event)
}

// --- What the editor host offers the policy. ---
export interface AgentVersionHost {
  /** What the open editor is now. confirmedVersion is the saved version it shows: a reading editor always, a writable one only while it is clean. */
  state(): { mode: "none" | "reader" | "writable"; clean: boolean; confirmedVersion?: string }
  /** The targets of a writable, clean editor. */
  targets(): LiveApplyTargets | undefined
  /** Today's path for an editor that only reads: open the latest saved version. */
  refreshReader(): Promise<void>
  /**
   * Replays the agent's commands in the read-only editor and moves the version it shows to `version`.
   * true: done (or the editor was replaced meanwhile by one that was read fresh); false: the caller opens the whole version.
   */
  replayReader?(commands: NativeExecuteCommand[], version: string, signal: AbortSignal): Promise<boolean>
  /** The editor is about to be changed by the agent's version: shown BEFORE the work starts, taken down when it ends or fails. */
  working?(on: boolean): void
  /** Says something to the person; undefined takes the message down. */
  notice(text: string | undefined): void
  /** The editor revision the host last saw, so a document that moved is not replayed onto. */
  baseRevision?(): string | undefined
  /**
   * Concurrent editing: replays the versions saved since the editor's base over its state (the person's
   * unsaved work included, their cells kept) and saves on top of the newest. Offered by a writable editor.
   */
  rebase?(event: AgentVersionEvent | undefined, signal: AbortSignal): Promise<RebaseOutcome>
  /** What a rebase came to, for the person: the cells they kept, or the choice when it could not be replayed. undefined event: after a save refused as moved. */
  rebased?(event: AgentVersionEvent | undefined, outcome: RebaseOutcome): void
  /** After a save refused as moved, the log had nothing newer: lift the refusal and save again. */
  retryMoved?(): void
}

/** Resolves after the next frame has been drawn (or soon, in a tab that draws none), so a note shown before heavy work is on screen before the page freezes. */
export function nextPaint(): Promise<void> {
  return new Promise(resolve => {
    let done = false
    const finish = () => { if (!done) { done = true; resolve() } }
    if (typeof requestAnimationFrame === "function") requestAnimationFrame(() => requestAnimationFrame(finish))
    setTimeout(finish, 100)
  })
}

export const AGENT_CHANGED_FINISH = "The agent changed the Sheet. Finish your edit; it is kept, and the agent's change is shown once your edit is saved."
export const AGENT_CHANGED_RELOAD = "The agent changed the Sheet, and the change could not be shown here. Reload the page before you edit."

/**
 * Decides what a new agent version does to the open editor. Rules:
 *  - one version is handled once, in order, one at a time;
 *  - a version that is not newer than what the editor shows is ignored (a reloaded chat shows old results);
 *  - a reading editor opens the latest saved version (the existing path);
 *  - a writable editor with anything unsaved NEVER takes the change: the latest one waits, a notice says so,
 *    and it is handled when the person's work is saved;
 *  - a clean writable editor replays the agent's commands only when the version is exactly the base + 1,
 *    otherwise (or when the replay fails) the person is told to reload; nothing of theirs is replaced.
 */
export function createAgentVersionQueue(host: AgentVersionHost) {
  const handled = new Set<string>()
  let chain: Promise<void> = Promise.resolve()
  let waiting: AgentVersionEvent | undefined
  let disposed = false
  const abort = new AbortController()

  const newerThanShown = (event: AgentVersionEvent): boolean => {
    const shown = host.state().confirmedVersion
    return shown === undefined || BigInt(event.version) > BigInt(shown)
  }

  /** viaLog: the change waited for the person's save, so the editor is no longer exactly one version behind it; only the operations log can bring it in. */
  async function handle(event: AgentVersionEvent, viaLog = false): Promise<void> {
    if (disposed) return
    const state = host.state()
    if (state.mode === "none") return
    if (state.mode === "reader") {
      host.notice(undefined)
      const shown = state.confirmedVersion
      if (shown !== undefined && BigInt(event.version) <= BigInt(shown)) return
      const commands = event.commandsOmitted ? undefined : event.commands
      const follows = shown !== undefined && BigInt(event.version) === BigInt(shown) + 1n && (event.baseVersion === undefined || event.baseVersion === shown)
      host.working?.(true)
      try {
        await nextPaint()
        if (disposed) return
        if (follows && commands && commands.length > 0 && host.replayReader) {
          // A failed replay is followed by the whole version, which replaces the document.
          if (await host.replayReader(commands, event.version, abort.signal).catch(() => false) || disposed) return
        }
        await host.refreshReader()
      } finally { host.working?.(false) }
      return
    }
    const reload = async () => { host.notice(AGENT_CHANGED_RELOAD) }
    // The agent's commands as it sent them (e.g. range.clear) are replayed only when the editor takes each as it is;
    // otherwise the operations log, in the editor's own names, brings the version in.
    const commandsGiven = !event.commandsOmitted && (event.commands?.length ?? 0) > 0 && event.contentHash !== undefined
      && event.commands!.every(c => (NATIVE_EXECUTE_COMMANDS as readonly string[]).includes(c.command))
    if (host.rebase && (viaLog || !state.clean || !commandsGiven)) {
      // Unsaved work: say at once that it is kept, until the replay over it is done (it never replaces it).
      host.notice(state.clean ? undefined : AGENT_CHANGED_FINISH)
      host.working?.(true)
      try {
        await nextPaint()
        if (disposed) return
        const outcome = await host.rebase(event, abort.signal)
        if (disposed) return
        // Not replayed for any reason: a person with unsaved work is never told to reload (they could lose it). The change
        // waits for their save and is handled again then; only a clean editor is told to reload.
        if (outcome.kind === "reload") {
          if (!state.clean) { waiting = event; host.notice(AGENT_CHANGED_FINISH) } else await reload()
          return
        }
        host.notice(undefined)
        host.rebased?.(event, outcome)
      } finally { host.working?.(false) }
      return
    }
    if (!state.clean) { waiting = event; host.notice(AGENT_CHANGED_FINISH); return }
    const targets = host.targets()
    const base = state.confirmedVersion
    if (!targets || base === undefined || BigInt(event.version) !== BigInt(base) + 1n || (event.baseVersion !== undefined && event.baseVersion !== base)) { await reload(); return }
    const commands = event.commandsOmitted ? undefined : event.commands
    if (event.contentHash === undefined) { await reload(); return }
    host.notice(undefined)
    host.working?.(true)
    try {
      await nextPaint()
      if (disposed) return
      const revision = host.baseRevision?.()
      await applyAgentVersion(targets, { version: event.version, contentHash: event.contentHash, commands, ...(revision === undefined ? {} : { baseRevision: revision }), reopen: reload, signal: abort.signal })
    } finally { host.working?.(false) }
  }

  // Something went wrong handling a version: unsaved work is kept and the change waits for its save; only a clean editor is told to reload.
  function failed(event: AgentVersionEvent): void {
    if (disposed) return
    const state = host.state()
    if (state.mode === "writable" && !state.clean) { waiting = event; host.notice(AGENT_CHANGED_FINISH) }
    else host.notice(AGENT_CHANGED_RELOAD)
  }

  return {
    push(event: AgentVersionEvent): void {
      if (disposed || !isNativeVersionText(event.version) || handled.has(event.version)) return
      if (!newerThanShown(event)) return
      handled.add(event.version)
      chain = chain.then(() => handle(event)).catch(() => failed(event))
    },
    /** The person's work was saved or dropped: a change that waited is handled now. */
    settled(): void {
      if (disposed || !waiting || host.state().mode !== "writable" || !host.state().clean) return
      const event = waiting
      waiting = undefined
      chain = chain.then(() => handle(event, true)).catch(() => failed(event))
    },
    /** A save of the person's was refused because the Sheet moved on: replay what was saved meanwhile, in turn with agent versions. */
    moved(): void {
      if (disposed || !host.rebase) return
      chain = chain.then(async () => {
        if (disposed || host.state().mode !== "writable") return
        host.working?.(true)
        try {
          const outcome = await host.rebase!(undefined, abort.signal)
          if (disposed) return
          // Nothing newer, or nothing that can be replayed here: the refusal is not left standing (autosave would never run
          // again) and the person is not told to reload; the server decides again, within the host's three tries.
          if (outcome.kind === "current" || outcome.kind === "reload") { host.retryMoved?.(); return }
          host.rebased?.(undefined, outcome)
        } finally { host.working?.(false) }
      }).catch(() => { if (!disposed) host.retryMoved?.() })
    },
    /** Resolves when everything pushed so far is handled (tests). */
    idle: () => chain,
    dispose(): void { disposed = true; waiting = undefined; abort.abort() },
  }
}

/** Reads the agent-version fields of a tool result body; anything malformed is simply absent. */
export function parseAgentVersionFields(body: Record<string, unknown>): Pick<AgentVersionEvent, "version" | "contentHash" | "baseVersion" | "commands" | "commandsOmitted"> | undefined {
  const version = typeof body.version === "number" && Number.isSafeInteger(body.version) && body.version > 0 ? String(body.version) : body.version
  if (!isNativeVersionText(version)) return undefined
  const out: ReturnType<typeof parseAgentVersionFields> = { version }
  if (typeof body.content_hash === "string" && /^sha256:[a-f0-9]{64}$/.test(body.content_hash)) out.contentHash = body.content_hash
  const base = typeof body.base_version === "number" && Number.isSafeInteger(body.base_version) && body.base_version > 0 ? String(body.base_version) : body.base_version
  if (isNativeVersionText(base)) out.baseVersion = base
  if (body.commands_omitted === true) out.commandsOmitted = true
  else if (Array.isArray(body.commands) && body.commands.length >= 1 && body.commands.length <= NATIVE_EXECUTE_MAX_COMMANDS
    && body.commands.every(c => typeof c === "object" && c !== null && !Array.isArray(c) && typeof (c as { command?: unknown }).command === "string")) out.commands = body.commands as NativeExecuteCommand[]
  return out
}
