import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { toPageView } from "@/hooks/use-pages"
import { DocumentCatalog } from "../document-catalog"

afterEach(cleanup)
it("opens a native Doc through its Page address without inventing revision or authorship",()=>{
  const select=vi.fn()
  render(<DocumentCatalog pages={[toPageView({id:"doc",slug:"notes",name:"Notes",has_document:true}),toPageView({id:"legacy",slug:"dashboard",name:"Dashboard"})]} onSelect={select} />)
  fireEvent.click(screen.getByRole("button",{name:"Notes Doc"}))
  expect(select).toHaveBeenCalledWith("notes")
  expect(screen.queryByText("Dashboard")).toBeNull()
  expect(screen.queryByText(/revision|author|saved/i)).toBeNull()
})
