/**
 * A creation custody whose release did not go: the editing lease that initialize gave
 * the creator, kept here (this tab only, in memory) so the editor that opens the Sheet can release exactly that
 * session and generation. A person who only views a new Sheet must not hold its lease; any other lease of theirs
 * (another tab editing) is never touched.
 */
export interface CreationCustody { token: string; generation: string }
const left = new Map<string, CreationCustody>()

export function noteUnreleasedCreation(pageId: string, custody: CreationCustody): void { left.set(pageId, { ...custody }) }

/** The custody left for this Page, once: the caller releases it. */
export function takeUnreleasedCreation(pageId: string): CreationCustody | undefined {
  const custody = left.get(pageId)
  left.delete(pageId)
  return custody
}
