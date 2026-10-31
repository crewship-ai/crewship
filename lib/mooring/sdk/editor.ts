/** Mooring SDK · in-frame editor. The code that runs inside the isolated,
 * credential-less editor iframe. Pure re-exports of the pilot protocol-1
 * modules; no Crewship authentication, HTTP, storage or UI dependencies. */
export { createEditorSDK, durableReceipt } from "../editor-sdk"
export type { DocumentState, DurableReceipt, EditorEnvironment, EditorSDKOptions, SaveStatus as EditorSaveStatus } from "../editor-sdk"
export { MOORING_PROTOCOL, MOORING_FRAME_ATTRIBUTES, verifyEditorHello, transferEditorPort, createCapabilityChannel, capabilityPort } from "../channel"
export type { CapabilityPort, CapabilityWindow, WindowEnvelope } from "../channel"
export { parseRevisionPush } from "../revision"
export { createBrowserEditorEnvironment, helloAfterLoad } from "../editor-connection"
export type { NativeEditorSDKOptions } from "../native-profile"
