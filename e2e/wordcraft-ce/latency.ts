import { createHash } from "node:crypto"
import { performance } from "node:perf_hooks"
import type { CDPSession, Page } from "@playwright/test"
import { summarizeMilliseconds } from "./metrics"

export interface Rectangle { x: number; y: number; width: number; height: number }
export interface ObservedWordCraftCommand {
  type: "command"
  op_id: string
  command: string
  base_revision: string
  target: Record<string, unknown>
  params: Record<string, unknown>
  sentAt: number
  transportURL: string
  transportId: string
}
export interface ObservedWordCraftSaved {
  type: "command_result"
  op_id: string
  revision: string
  status: "saved"
  durability: "committed"
  savedAt: number
  transportURL: string
  transportId: string
}

const isRecord = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value)
const u64 = (value: unknown): value is string => typeof value === "string" && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18_446_744_073_709_551_615n
const identifier = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9:._-]{0,255}$/.test(value)
const exact = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).length === keys.length && keys.every(key => key in value)

/** Passive Chromium Network observer. No route mocking, request injection,
 * postMessage, engine evaluation or socket sends. Start BEFORE navigation so
 * every observed frame is tied to its actual WebSocket URL. */
export class WordCraftTransportObserver {
  private session?: CDPSession
  private readonly sockets = new Map<string, string>()
  private readonly commands: ObservedWordCraftCommand[] = []
  private readonly receipts: ObservedWordCraftSaved[] = []
  private readonly changed = new Set<() => void>()
  private fatal?: Error
  private closed = false
  private readonly timeoutMs: number

  constructor(private readonly page: Page, private readonly options: { timeoutMs?: number; webSocketURL?: string } = {}) {
    this.timeoutMs = options.timeoutMs ?? 30_000
    if (!Number.isFinite(this.timeoutMs) || this.timeoutMs <= 0) throw new Error("invalid_transport_observation_timeout")
  }

  async start(): Promise<void> {
    if (this.session || this.closed) throw new Error("transport_observer_already_started_or_closed")
    this.session = await this.page.context().newCDPSession(this.page)
    this.session.on("Network.webSocketCreated", event => { this.sockets.set(event.requestId, event.url) })
    this.session.on("Network.webSocketFrameSent", event => this.frame("sent", event.requestId, event.response.opcode, event.response.payloadData))
    this.session.on("Network.webSocketFrameReceived", event => this.frame("received", event.requestId, event.response.opcode, event.response.payloadData))
    await this.session.send("Network.enable")
  }

  cursor(): number { return this.commands.length }

  commandSince(cursor: number, commandId: string): Promise<ObservedWordCraftCommand> {
    if (!Number.isSafeInteger(cursor) || cursor < 0 || cursor > this.commands.length) return Promise.reject(new Error("invalid_command_observation_cursor"))
    return this.wait(() => this.commands.slice(cursor).find(command => command.command === commandId), `no actual C1 command frame for ${commandId}`)
  }

  /** Supply the original transportId when multiple transports were observed.
   * Callers omitting it must still compare the returned socket to the command. */
  saved(opId: string, transportId?: string): Promise<ObservedWordCraftSaved> {
    return this.wait(() => this.receipts.find(receipt => receipt.op_id === opId && (transportId === undefined || receipt.transportId === transportId)), `no committed C1 receipt for original op_id ${opId}`)
  }

  async close(): Promise<void> {
    this.closed = true
    this.changed.forEach(listener => listener())
    await this.session?.detach()
  }

  private frame(direction: "sent" | "received", requestId: string, opcode: number, payload: string): void {
    const observedAt = performance.now()
    const transportURL = this.sockets.get(requestId)
    if (!transportURL || opcode !== 1 || payload.length > 1_048_576) return
    let url: URL
    try { url = new URL(transportURL) } catch { return }
    if (this.options.webSocketURL ? transportURL !== this.options.webSocketURL : url.host !== new URL(this.page.url()).host || !url.pathname.includes("/mooring/")) return
    // Admission credentials may live in a WS query. Never expose them through
    // returned trace records or an assertion failure; transportId correlates frames.
    const publicSocketURL = `${url.origin}${url.pathname}`
    let value: unknown
    try { value = JSON.parse(payload) } catch { return }
    if (!isRecord(value)) return
    if (direction === "sent" && value.type === "command") {
      if (!exact(value, ["type", "op_id", "command", "base_revision", "target", "params"]) || !identifier(value.op_id) || typeof value.command !== "string" || !u64(value.base_revision) || !isRecord(value.target) || !isRecord(value.params)) {
        this.fail("actual C1 command frame does not match the acceptance transport contract")
        return
      }
      this.commands.push({ type: "command", op_id: value.op_id, command: value.command, base_revision: value.base_revision, target: value.target, params: value.params, sentAt: observedAt, transportURL: publicSocketURL, transportId: requestId })
      this.changed.forEach(listener => listener())
    }
    if (direction === "received" && value.type === "command_result") {
      if (!exact(value, ["type", "op_id", "revision", "status", "durability"]) || !identifier(value.op_id) || !u64(value.revision) || value.status !== "saved" || value.durability !== "committed") {
        this.fail("actual C1 result lacks a committed Saved receipt")
        return
      }
      this.receipts.push({ type: "command_result", op_id: value.op_id, revision: value.revision, status: "saved", durability: "committed", savedAt: observedAt, transportURL: publicSocketURL, transportId: requestId })
      this.changed.forEach(listener => listener())
    }
  }

  private fail(message: string): void { this.fatal = new Error(message); this.changed.forEach(listener => listener()) }
  private wait<T>(find: () => T | undefined, description: string): Promise<T> {
    if (!this.session) return Promise.reject(new Error("transport_observer_not_started"))
    return new Promise((resolve, reject) => {
      const cleanup = () => { clearTimeout(timer); this.changed.delete(check) }
      const check = () => {
        if (this.fatal || this.closed) { cleanup(); reject(this.fatal ?? new Error("transport_observer_closed")); return }
        const value = find()
        if (value !== undefined) { cleanup(); resolve(value) }
      }
      const timer = setTimeout(() => { cleanup(); reject(new Error(description)) }, this.timeoutMs)
      this.changed.add(check)
      check()
    })
  }
}

export interface WordCraftLatencyState {
  engine_sha: string
  revision: string
  principal_id: string
  document: { body: readonly { kind: string; text?: string }[] }
}
export interface WordCraftLatencyOptions {
  writer: Page
  peer: Page
  observer: Pick<WordCraftTransportObserver, "cursor" | "commandSince" | "saved">
  samples: number
  timeoutMs: number
  engineSha: string
  writerCrop: Rectangle
  peerCrop: Rectangle
  prepareBlank(): Promise<void>
  focusWriter(): Promise<void>
  undoOwn(): Promise<void>
  readState(page: Page): Promise<WordCraftLatencyState>
  waitSaved(page: Page, revision: string, opId: string): Promise<void>
  waitRendered(page: Page, revision: string): Promise<void>
  canvasBounds(page: Page): Promise<Rectangle>
}

export interface WordCraftLatencySample {
  op_id: string
  revision: string
  keyDispatchedAt: number
  sendObservedAt: number
  durableObservedAt: number
  writerPixelsObservedAt: number
  peerPixelsObservedAt: number
}

function assertCrop(crop: Rectangle, bounds: Rectangle): void {
  if (![crop.x, crop.y, crop.width, crop.height, bounds.x, bounds.y, bounds.width, bounds.height].every(Number.isFinite) || crop.width <= 0 || crop.height <= 0 || crop.x < bounds.x || crop.y < bounds.y || crop.x + crop.width > bounds.x + bounds.width || crop.y + crop.height > bounds.y + bounds.height) throw new Error("glyph-only crop must fit inside the actual canvas")
}

async function capture(page: Page, clip: Rectangle): Promise<Buffer> {
  return page.screenshot({ clip, type: "png", scale: "css", animations: "disabled", caret: "hide" })
}

async function observePixels(page: Page, clip: Rectangle, reference: Buffer, timeoutMs: number, canvasBounds: (page: Page) => Promise<Rectangle>): Promise<number> {
  const deadline = performance.now() + timeoutMs
  while (performance.now() < deadline) {
    assertCrop(clip, await canvasBounds(page))
    const pixels = await capture(page, clip)
    const observedAt = performance.now()
    if (pixels.equals(reference) && observedAt <= deadline) return observedAt
    await new Promise(resolve => setTimeout(resolve, 10))
  }
  throw new Error("actual canvas glyph crop did not match the confirmed W reference")
}

/** Real browser measurement only. Callback implementations must use the actual
 * command host; absent transport, identity, paint or glyph evidence is a failure.
 * Screenshot completion and CDP callback receipt are observation upper bounds,
 * not exact compositor or server commit instants. */
export async function runWordCraftLatency(options: WordCraftLatencyOptions) {
  const { writer, peer, observer, writerCrop, peerCrop } = options
  if (!Number.isSafeInteger(options.samples) || options.samples < 1 || options.samples > 1_000 || !Number.isFinite(options.timeoutMs) || options.timeoutMs <= 0 || !/^[a-f0-9]{40}$/.test(options.engineSha)) throw new Error("invalid latency measurement configuration")
  if (writer.context() === peer.context()) throw new Error("latency peers require separate browser contexts")
  for (const callback of [options.prepareBlank, options.focusWriter, options.undoOwn, options.readState, options.waitSaved, options.waitRendered, options.canvasBounds]) if (typeof callback !== "function") throw new Error("latency measurement requires real integration callbacks")
  assertCrop(writerCrop, await options.canvasBounds(writer))
  assertCrop(peerCrop, await options.canvasBounds(peer))
  const captureCanvas = async (page: Page, clip: Rectangle) => {
    assertCrop(clip, await options.canvasBounds(page))
    return capture(page, clip)
  }

  const assertState = async (text: string, revision?: string) => {
    const [a, b] = await Promise.all([options.readState(writer), options.readState(peer)])
    if (a.engine_sha !== options.engineSha || b.engine_sha !== options.engineSha || !a.principal_id || !b.principal_id || a.principal_id === b.principal_id || !u64(a.revision) || a.revision !== b.revision || (revision !== undefined && a.revision !== revision)) throw new Error("actual engine, distinct principal or confirmed revision evidence is missing")
    for (const state of [a, b]) if (state.document.body.length !== 1 || state.document.body[0].kind !== "para" || state.document.body[0].text !== text) throw new Error("latency fixture must be the actual single blank/W paragraph")
    await Promise.all([options.waitRendered(writer, a.revision), options.waitRendered(peer, b.revision)])
    return a.revision
  }
  const insert = async (baseRevision: string) => {
    await options.focusWriter()
    const cursor = observer.cursor()
    const keyDispatchedAt = performance.now()
    await writer.keyboard.type("W")
    const command = await observer.commandSince(cursor, "text.insert")
    if (command.base_revision !== baseRevision || Object.keys(command.params).length !== 1 || command.params.text !== "W") throw new Error("observed command is not the measured UI W insertion at the confirmed base")
    return { command, keyDispatchedAt }
  }
  const confirm = async (command: ObservedWordCraftCommand) => {
    const receipt = await observer.saved(command.op_id, command.transportId)
    if (receipt.transportURL !== command.transportURL || receipt.transportId !== command.transportId || receipt.savedAt < command.sentAt || BigInt(receipt.revision) <= BigInt(command.base_revision)) throw new Error("committed receipt does not match the original measured operation")
    await options.waitSaved(writer, receipt.revision, receipt.op_id)
    await assertState("W", receipt.revision)
    return receipt
  }

  await options.prepareBlank()
  let baseRevision = await assertState("")
  const [blankWriter, blankPeer] = await Promise.all([captureCanvas(writer, writerCrop), captureCanvas(peer, peerCrop)])
  const warm = await insert(baseRevision)
  await confirm(warm.command)
  const [referenceWriter, referencePeer] = await Promise.all([captureCanvas(writer, writerCrop), captureCanvas(peer, peerCrop)])
  if (referenceWriter.equals(blankWriter) || referencePeer.equals(blankPeer)) throw new Error("configured crop is not a glyph oracle: blank and confirmed W are identical")
  // A caller must configure glyph-only crops excluding the native caret and
  // selection. This second observation detects some unstable crops; it is not
  // an automatic caret mask or proof that an arbitrary crop contains a glyph.
  await new Promise(resolve => setTimeout(resolve, 600))
  const [stableWriter, stablePeer] = await Promise.all([captureCanvas(writer, writerCrop), captureCanvas(peer, peerCrop)])
  if (!referenceWriter.equals(stableWriter) || !referencePeer.equals(stablePeer)) throw new Error("configured W glyph crop is unstable; exclude caret, selection and animation")

  const reset = async () => {
    await options.undoOwn()
    baseRevision = await assertState("")
    const [a, b] = await Promise.all([captureCanvas(writer, writerCrop), captureCanvas(peer, peerCrop)])
    if (!a.equals(blankWriter) || !b.equals(blankPeer)) throw new Error("UI undo did not restore the exact blank glyph oracle")
    await new Promise(resolve => setTimeout(resolve, 600))
    const [stableA, stableB] = await Promise.all([captureCanvas(writer, writerCrop), captureCanvas(peer, peerCrop)])
    if (!a.equals(stableA) || !b.equals(stableB)) throw new Error("configured blank glyph crop is unstable; exclude caret, selection and animation")
  }
  await reset()
  const samples: WordCraftLatencySample[] = []
  for (let index = 0; index < options.samples; index++) {
    await options.focusWriter()
    const cursor = observer.cursor()
    const keyDispatchedAt = performance.now()
    // Begin both visual observations with the UI key; never substitute receipt
    // time, read API text or rendered-revision metadata for visible pixels.
    const key = writer.keyboard.type("W")
    const pending = Promise.all([
      key,
      observePixels(writer, writerCrop, referenceWriter, options.timeoutMs, options.canvasBounds),
      observePixels(peer, peerCrop, referencePeer, options.timeoutMs, options.canvasBounds),
      observer.commandSince(cursor, "text.insert"),
    ])
    const [, writerPixelsObservedAt, peerPixelsObservedAt, command] = await pending
    if (command.base_revision !== baseRevision || Object.keys(command.params).length !== 1 || command.params.text !== "W") throw new Error("measured UI insertion does not match the observed command")
    const receipt = await confirm(command)
    if (command.sentAt < keyDispatchedAt || writerPixelsObservedAt < keyDispatchedAt || peerPixelsObservedAt < keyDispatchedAt) throw new Error("latency observations do not share the expected monotonic clock")
    samples.push({ op_id: command.op_id, revision: receipt.revision, keyDispatchedAt, sendObservedAt: command.sentAt, durableObservedAt: receipt.savedAt, writerPixelsObservedAt, peerPixelsObservedAt })
    await reset()
  }
  return {
    schemaVersion: 1,
    engine_sha: options.engineSha,
    method: {
      clock: "node.performance.now; milliseconds from one runner process",
      key: "UI keyboard.type dispatch; no injected engine operation",
      visual: "exact PNG match of configured glyph-only canvas crops against confirmed UI W reference; screenshot completion is an observation upper bound",
      durable: "original op_id and correlated actual transport response; HTTP C1 requires host_committed receipt and exact original input digest; callback receipt is an observation upper bound",
      peerChangeOrigin: "UI key dispatch, same origin as writer key-to-visible measurement",
      exclusions: "No network/read response or rendered-revision metadata is counted as visibility. Golden setup and confirmed UI undo are outside timed samples.",
    },
    crops: { writer: writerCrop, peer: peerCrop },
    oracleSha256: Object.fromEntries(Object.entries({ blankWriter, blankPeer, referenceWriter, referencePeer }).map(([name, bytes]) => [name, createHash("sha256").update(bytes).digest("hex")])),
    metrics: {
      keyToWriterPixels: summarizeMilliseconds(samples.map(sample => sample.writerPixelsObservedAt - sample.keyDispatchedAt)),
      sentToDurableSaved: summarizeMilliseconds(samples.map(sample => sample.durableObservedAt - sample.sendObservedAt)),
      changeToPeerPixels: summarizeMilliseconds(samples.map(sample => sample.peerPixelsObservedAt - sample.keyDispatchedAt)),
    },
    samples,
  }
}
