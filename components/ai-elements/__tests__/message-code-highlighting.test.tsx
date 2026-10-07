import { render, waitFor } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { MessageResponse } from "../message"

describe("message code highlighting", () => {
  it("highlights a growing code fence and retains its completed contents", async () => {
    const { container, rerender } = render(
      <MessageResponse isAnimating>{"```typescript\nconst first = 1;\n"}</MessageResponse>,
    )
    await waitFor(() => expect(container.querySelector("pre code")).toHaveTextContent("const first = 1;"))

    const completed = "```typescript\nconst first = 1;\nconst second = first + 2;\n```"
    rerender(<MessageResponse isAnimating>{completed}</MessageResponse>)
    await waitFor(() => {
      expect(container.querySelector("pre code")).toHaveTextContent("const second = first + 2;")
      expect(container.querySelector("pre code span[style]")).not.toBeNull()
    })
    rerender(<MessageResponse isAnimating={false}>{completed}</MessageResponse>)
    expect(container.querySelector("pre code")).toHaveTextContent("const first = 1;")
    expect(container.querySelector("pre code")).toHaveTextContent("const second = first + 2;")
  })

  it("does not reuse highlighted tokens from a block with different middle contents", async () => {
    const prefix = `// ${"same prefix ".repeat(30)}\n`
    const suffix = `\n// ${"same suffix ".repeat(30)}`
    const block = (middle: string) => `\`\`\`typescript\n${prefix}${middle}${suffix}\n\`\`\``
    const { container, rerender } = render(<MessageResponse>{block('const value = "before";')}</MessageResponse>)
    await waitFor(() => expect(container.querySelector("pre code span[style]")).not.toBeNull())
    expect(container.querySelector("pre code")).toHaveTextContent('const value = "before";')

    rerender(<MessageResponse>{block('const value = "after!";')}</MessageResponse>)
    await waitFor(() => {
      expect(container.querySelector("pre code")).toHaveTextContent('const value = "after!";')
      expect(container.querySelector("pre code")).not.toHaveTextContent('const value = "before";')
      expect(container.querySelector("pre code span[style]")).not.toBeNull()
    })
  })
})
