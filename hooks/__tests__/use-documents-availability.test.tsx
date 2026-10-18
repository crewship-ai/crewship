import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { useDocumentsAvailability } from "../use-documents-availability"
const fetchMock=vi.hoisted(()=>vi.fn())
vi.mock("@/lib/api-fetch",()=>({apiFetch:fetchMock}))
afterEach(()=>fetchMock.mockReset())
it.each([true,false,"true",undefined])("requires confirmed boolean availability: %s",async(enabled)=>{
  fetchMock.mockResolvedValue({ok:true,json:async()=>({enabled})})
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  const wrapper=({children}:{children:ReactNode})=>createElement(QueryClientProvider,{client},children)
  const {result,unmount}=renderHook(()=>useDocumentsAvailability("workspace"),{wrapper})
  await waitFor(()=>expect(result.current.isSuccess).toBe(true))
  expect(result.current.data).toBe(enabled===true)
  unmount();client.clear()
})
it("keeps failure unavailable instead of claiming service health",async()=>{
  fetchMock.mockResolvedValue({ok:false,status:503})
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  const wrapper=({children}:{children:ReactNode})=>createElement(QueryClientProvider,{client},children)
  const {result,unmount}=renderHook(()=>useDocumentsAvailability("workspace"),{wrapper})
  await waitFor(()=>expect(result.current.isError).toBe(true))
  await waitFor(()=>expect(result.current.data).not.toBe(true))
  unmount();client.clear()
})

it.each([403,503])("withdraws confirmed availability when an authorized refresh fails with %s",async(status)=>{
  fetchMock.mockResolvedValue({ok:true,json:async()=>({enabled:true})})
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  const wrapper=({children}:{children:ReactNode})=>createElement(QueryClientProvider,{client},children)
  const {result,unmount}=renderHook(()=>useDocumentsAvailability("workspace"),{wrapper})
  await waitFor(()=>expect(result.current.data).toBe(true))
  fetchMock.mockResolvedValue({ok:false,status})
  await act(async()=>{
    const refreshed=await result.current.refetch()
    expect(refreshed.isError).toBe(true)
  })
  await waitFor(()=>expect(result.current.data).not.toBe(true))
  unmount();client.clear()
})
