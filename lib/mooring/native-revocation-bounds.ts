/** How often an open native editor asks the server whether it is still admitted. */
export const NATIVE_REAUTHORIZE_INTERVAL_MS=30_000
/** One attempt may not outlive the next one, so a stalled request ends by the
 * time the following attempt is due. No separate product number: this is the
 * existing 30 s cadence. */
export const NATIVE_REAUTHORIZE_DEADLINE_MS=NATIVE_REAUTHORIZE_INTERVAL_MS
/** Worst case from a committed revocation until an open editor stops showing
 * content and offering its draft, with the metadata stream silent: the attempt
 * after the last confirmation starts one interval later and ends one deadline
 * after that. A healthy stream reports within craftNativeEventInterval (5 s). */
export const NATIVE_DETECTION_BOUND_MS=NATIVE_REAUTHORIZE_INTERVAL_MS+NATIVE_REAUTHORIZE_DEADLINE_MS
/** Fresh admission deadline. A handoff spends it from the moment the writer has
 * confirmed its final save and retired (docs/specs/craft-native.md). */
export const NATIVE_ADMISSION_DEADLINE_MS=60_000
