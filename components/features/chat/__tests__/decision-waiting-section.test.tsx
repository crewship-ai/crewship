import React from "react"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import pending from "@/internal/api/testdata/chat-e2/decision-waitpoint-pending.json"
import expired from "@/internal/api/testdata/chat-e2/decision-waitpoint-expired.json"
import { DecisionRefSchema } from "@/lib/chat-e2-cards"
import { DecisionWaitingSection } from "../decision-waiting-section"
afterEach(cleanup)
it("opens the identical pending source and removes terminal decisions", () => {
 const reference = DecisionRefSchema.parse(pending)
 const onOpen = vi.fn()
 const view = render(<DecisionWaitingSection references={[reference]} rechecking={false} onOpen={onOpen} />)
 fireEvent.click(screen.getByRole("button",{name:reference.decision.title}))
 expect(onOpen).toHaveBeenCalledWith(reference)
 view.rerender(<DecisionWaitingSection references={[DecisionRefSchema.parse(expired)]} rechecking={false} onOpen={onOpen} />)
 expect(screen.queryByRole("region")).toBeNull()
 expect(screen.queryByText("Waiting for you")).toBeNull()
})
it("reveals no existence or count after authorized snapshot eviction", () => {
 const view = render(<DecisionWaitingSection references={[]} rechecking={false} onOpen={vi.fn()} />)
 expect(view.container).toBeEmptyDOMElement()
})
