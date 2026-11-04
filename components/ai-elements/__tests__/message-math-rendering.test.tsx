import { render, waitFor } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { MessageResponse } from "../message"

describe("message math rendering", () => {
  it("renders display math through the installed Streamdown math plugin", async () => {
    const { container } = render(<MessageResponse>{"$$\nx^2 + y^2 = z^2\n$$"}</MessageResponse>)
    await waitFor(() => expect(container.querySelector(".katex math")).not.toBeNull())
    expect(container.querySelector(".katex-error")).toBeNull()
    expect(container.querySelector("annotation")).toHaveTextContent("x^2 + y^2 = z^2")
  })

  it("refuses untrusted math commands that load an external resource", async () => {
    const { container } = render(
      <MessageResponse>{String.raw`$$
\includegraphics{https://example.test/untrusted-math.png}
$$`}</MessageResponse>,
    )
    await waitFor(() => expect(container.querySelector(".katex")).not.toBeNull())
    expect(container.querySelector('img[src*="untrusted-math"]')).toBeNull()
  })
})
