import type { EditorPane, EditorSection, PageCapabilities } from "@/lib/pages/editor-contract"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * One shape for all four sections, so the shell mounts them interchangeably
 * and a section cannot quietly grow a dependency on the shell's internals.
 *
 * `page` is the raw wire record rather than the rendered `PageView`: the
 * renderer's view is lossy on purpose (it drops `public`, flattens
 * provenance) and an editor deriving a save from it deletes fields nobody
 * touched — the same trap `usePage`'s own comment warns about.
 */
export interface EditorSectionProps {
  workspaceId: string
  slug: string
  page: WirePageDetail | null
  capabilities: PageCapabilities
  /**
   * Move to another section without leaving the editor. Data & actions links
   * to Access this way rather than growing a second token form: minting a
   * producer credential is a permission, and it lives in one place.
   */
  onNavigate: (section: EditorSection) => void
  /**
   * Which workspace this section is showing. Only Content uses `preview` —
   * the candidate's build gets a surface of its own rather than being wedged
   * beside a form and a diff in three narrow columns.
   */
  pane: EditorPane
  onPaneChange: (pane: EditorPane) => void
  /**
   * Leave the editor and go back to viewing the Page.
   *
   * Two sections need a way out that is not another section, and neither
   * could express it: "Close without publishing" ends a review, and deleting
   * a Page leaves nothing behind to edit. Without this they either become a
   * silent no-op or reach for the router directly, which in this shell would
   * unmount the rail the editor was careful not to disturb.
   */
  onLeaveEditor: () => void
  /**
   * The Page this editor was editing no longer exists. The shell returns to
   * the overview; a section must not route away by itself.
   */
  onPageDeleted: () => void
  /**
   * Raise while this section holds edits that are not written yet. The shell
   * guards Page switches, section switches and Back on it. It is not a promise
   * about closing the browser: `beforeunload` cannot make one.
   */
  onDirtyChange: (dirty: boolean) => void
}
