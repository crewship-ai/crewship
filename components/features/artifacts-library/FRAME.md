# ArtifactFrame: one frame for every kind

Every open artifact renders inside `ArtifactFrame` (`frame/`). The frame owns
the header, the status chip, the title menu, the right drawer, the slots and
the access gates. A kind owns only its **body**. It follows Harbor 2 v87.

## Kinds

`kinds.ts` is the only registry: label, plural, lucide icon, accent token and
family (Artifact or Craft) for `docs | dashboard | slides | design | app |
word | sheet`. `kindOf(item)` maps storage (LibraryKind, server content type,
native flag, application, design-system manifest) to `{ kind, mode?, badge? }`.
Use `KindTile`, `KindBadge` and `KindThumb`. Never show "Page" as a label.

## Frame props

```tsx
<ArtifactFrame name kind={kindOf(item)} folder caps onHome href? onShare? presence?
  info? statusFoot? statusActions? signInHref? defaultDrawer? layout?>
  {body}
</ArtifactFrame>
```

`layout` is pinned only in tests. The app follows `useLayoutBreakpoint`:
phone < 768 px, tablet < 1280 px, desktop.

## Capabilities: the source of truth

```ts
interface FrameCapabilities {
  access: "ok" | "signedout" | "denied" | "missing"  // anything but ok shows its gate, never the body
  canEdit: boolean                // server-confirmed; unknown is false
  status: FrameStatus | null      // null hides the chip
  agent: AgentSupport | null      // null hides the Agent tab
  actions?: FrameAction[]         // title-menu entries after Share and Copy link
}
interface AgentSupport { ask?: { chatHref(prompt) }; propose?: AgentSend; edit?: AgentSend;
  scopes?; suggestions?; permission? }
```

Rules: an action or mode the backend lacks is **left out**. One this person
may not use stays **disabled with its reason** (`disabledReason`; a mode
without `canEdit` reads "Needs edit access"). Build the capabilities from
real answers (availability, lease, catalog). Never use placeholders.

## Status keys

`live saving saved offline readonly free editing held agent proposal draft
stale refreshing unconfirmed error` (`frame/status.ts`, with tone, icon,
label and detail). `{ key, label?, detail? }` overrides the words. **Saved**
appears only after the server has acknowledged a version. Until then the chip
stays on Saving… or Not confirmed. For Craft, `craftFrameStatus(hostState)`
applies this rule.

## Registering drawer tabs and agent modes from the body

The drawer shows the kind's tabs, then Agent, Comments, History and Info. A
tab exists only while the body mounts its content:

```tsx
<ArtifactDrawerSection tab="history" label="History" icon={History}>…</ArtifactDrawerSection>
<ArtifactDrawerSection tab="layers" label="Layers" order={0}>…</ArtifactDrawerSection>  // a kind tab
<ArtifactAgentMode mode="edit">{craftAgentStartForm}</ArtifactAgentMode>                // only while admitted
```

`history` adds the header button and the "Version history" menu item.
`comments` adds the Comments button. Info comes from the `info` prop, which
mounts only once Info is opened. The body may call `useArtifactFrame()` to
read `layout`, call `openDrawer(tab)` or read `drawerTab`.

## Slots

Place floating chrome with `<ArtifactSlot name label>`, and use
`ArtifactTool` / `ArtifactToolSeparator` inside it.

| Slot | Wide | Phone |
|---|---|---|
| `view` | top left | top row left of `output`, scrolls sideways |
| `output` | top right | top right, in one row with `view` (never over the content) |
| `tools` | bottom centre | full-width bar at the bottom |
| `viewport` | bottom right | hidden; pinch zoom instead |
| `nav` | bottom left | above the tools |
| `panel` | left column under `view` | bottom sheet, 62 % high |

On a phone the header keeps only back, the title (its menu) and the status
chip, plus Agent, Comments and ⋯. Every action is in the menu, and the drawer
fills the screen. On a tablet the drawer covers the stage from the right.

## Agent paths in use (no new endpoints)

- **Ask**, every kind: `/chat?page=<slug>&prompt=…&draft=1`.
- **Edit**, native Word and Sheet: the Craft agent task
  (`POST /api/v1/pages/@<id>/craft/tasks`). The editor host mounts it through
  `placeAgentStart` when availability reports `agents_enabled` and the person
  can edit.
- Projects, dashboards, slides and design send comments to an agent from
  their own viewer (`send_to_agent`, `useArtifactAgentDispatch`). They can
  register a Propose form with `ArtifactAgentMode` when they move into the
  drawer.

## Worked example: a kind with a canvas

```tsx
function BoardDetail({ item }: { item: LibraryItem }) {
  const live = useBoardLive(item)                      // the kind's own hook
  const caps: FrameCapabilities = {
    access: "ok", canEdit: live.canEdit,
    status: live.connected ? { key: "live" } : { key: "offline" },
    agent: { ask: { chatHref: p => chatAskHref(ws, item.slug, p) } },
    actions: [{ id: "export", label: "Export…", onSelect: live.export,
      ...(live.canExport ? {} : { disabledReason: "Only editors can export" }) }],
  }
  return <ArtifactFrame name={item.name} kind={kindOf(item)} folder={item.folder?.name ?? null} caps={caps} onHome={back}>
    <BoardCanvas board={live.board} />
    <ArtifactSlot name="tools" label="Board tools"><ArtifactTool icon={StickyNote} label="Note" /></ArtifactSlot>
    {live.history && <ArtifactDrawerSection tab="history" label="History" icon={History}><BoardHistory /></ArtifactDrawerSection>}
  </ArtifactFrame>
}
```

Tests: `__tests__/artifact-frame.test.tsx` covers the slots, gates, hidden
modes and the status rule.
