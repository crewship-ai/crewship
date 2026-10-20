import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

const stick = vi.hoisted(() => ({ props: {} as Record<string, unknown>, scrollToBottom: vi.fn() }))
vi.mock("use-stick-to-bottom", () => {
  const StickToBottom = (props: Record<string, unknown>) => { stick.props = props; return <div /> }
  StickToBottom.Content = () => null
  return { StickToBottom, useStickToBottomContext: () => ({ isAtBottom: false, scrollToBottom: stick.scrollToBottom }) }
})
const motion = vi.hoisted(() => ({ reduce: false as boolean | null }))
vi.mock("motion/react", async (importOriginal) => ({ ...(await importOriginal<typeof import("motion/react")>()), useReducedMotionConfig: () => motion.reduce }))
import { Conversation, ConversationScrollButton } from "../conversation"

afterEach(() => { cleanup(); motion.reduce = false; stick.scrollToBottom.mockReset() })

it.each([[false, "smooth"], [null, "smooth"], [true, "instant"]] as const)("follows the transcript with reduced motion %s as %s", (reduce, behaviour) => {
  motion.reduce = reduce
  render(<Conversation />)
  expect(stick.props).toMatchObject({ initial: behaviour, resize: behaviour })
})
it.each([[false, undefined], [true, "instant"]] as const)("Scroll to bottom with reduced motion %s", (reduce, arg) => {
  motion.reduce = reduce
  render(<ConversationScrollButton />)
  fireEvent.click(screen.getByRole("button", { name: "Scroll to bottom" }))
  expect(stick.scrollToBottom).toHaveBeenCalledWith(...(arg ? [arg] : []))
})
