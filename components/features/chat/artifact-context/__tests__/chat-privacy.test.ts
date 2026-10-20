import { expect, it } from "vitest"
import { PARTICIPANTS_MAX_AGE_MS, deriveChatPrivacy, type ParticipantsLoad } from "../chat-privacy"

const base = { scope: "w:c1", sessionKind: "direct", sharedRestricted: false, probeSettled: true, profileKnown: true, currentUserId: "me" }
const loaded = (patch: Partial<ParticipantsLoad> = {}): ParticipantsLoad => ({ scope: "w:c1", status: "ok", userIds: [], at: 1000, ...patch })

it("is private only on positive signals: this session's participants loaded ok and nobody else in the chat", () => {
  expect(deriveChatPrivacy({ ...base, participants: loaded() })).toBe("private")
  expect(deriveChatPrivacy({ ...base, participants: loaded({ userIds: ["me"] }) })).toBe("private")
})
it("more than one human is not private", () => {
  expect(deriveChatPrivacy({ ...base, participants: loaded({ userIds: ["me", "you"] }) })).toBe("not_private")
  expect(deriveChatPrivacy({ ...base, participants: loaded({ userIds: ["you"] }) })).toBe("not_private")
})
it("a failed or non-ok participants read is unverified, never private", () => {
  expect(deriveChatPrivacy({ ...base, participants: loaded({ status: "error" }) })).toBe("unverified")
})
it("pending reads are checking, including participants of another session", () => {
  expect(deriveChatPrivacy({ ...base, participants: null })).toBe("checking")
  expect(deriveChatPrivacy({ ...base, participants: loaded({ scope: "w:other" }) })).toBe("checking")
  expect(deriveChatPrivacy({ ...base, probeSettled: false, participants: loaded() })).toBe("checking")
})
it("an unanswered profile probe is unverified; a restricted group or a non-direct chat is not private", () => {
  expect(deriveChatPrivacy({ ...base, profileKnown: false, participants: loaded() })).toBe("unverified")
  expect(deriveChatPrivacy({ ...base, sharedRestricted: true, participants: loaded() })).toBe("not_private")
  expect(deriveChatPrivacy({ ...base, sessionKind: "issue", participants: loaded() })).toBe("not_private")
})
it("without a known current user nothing is private", () => {
  expect(deriveChatPrivacy({ ...base, currentUserId: null, participants: loaded() })).toBe("unverified")
})
it("a participants read older than the limit no longer counts, so a value captured once is not a signal", () => {
  expect(deriveChatPrivacy({ ...base, participants: loaded(), now: 1000 + PARTICIPANTS_MAX_AGE_MS })).toBe("private")
  expect(deriveChatPrivacy({ ...base, participants: loaded(), now: 1001 + PARTICIPANTS_MAX_AGE_MS })).toBe("checking")
})
