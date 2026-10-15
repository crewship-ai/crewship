/** This identifies the persisted application profile, not editor write authority.
 * New profiles need an explicit reader/migration; opening must never rewrite one.
 */
export const BOARD_FORMAT = 'crewship-board-excalidraw-0.18.1-v1'
export const BOARD_EDITOR_VERSION = '0.18.1'
export const BOARD_CONVERTER_VERSION = '2.2.2'
export class BoardCompatibilityError extends Error {}
export function assertBoardCompatibility(value: { format?: unknown }): void {
  if (value.format !== BOARD_FORMAT) throw new BoardCompatibilityError('This Board uses an unsupported saved format. Open it with a compatible Crewship release; no scene was migrated or saved.')
}
