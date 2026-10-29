import {createElement,type ReactNode} from 'react'
import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {act,renderHook,waitFor} from '@testing-library/react'
import {afterEach,expect,it,vi} from 'vitest'
import {useDocumentsAvailability} from '../use-documents-availability'
const fetchMock=vi.hoisted(()=>vi.fn())
vi.mock('@/lib/api-fetch',()=>({apiFetch:fetchMock}))
afterEach(()=>fetchMock.mockReset())
function mount(){const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}}),wrapper=({children}:{children:ReactNode})=>createElement(QueryClientProvider,{client},children);return {...renderHook(()=>useDocumentsAvailability('workspace'),{wrapper}),client}}
it.each([true,false,'true',undefined,null])('requires literal true for independent Sheet availability: %s',async(grid)=>{
 fetchMock.mockResolvedValue({ok:true,json:async()=>({enabled:false,wordcraft_native:false,gridcraft_native:grid})})
 const {result,unmount,client}=mount();await waitFor(()=>expect(result.current.isSuccess).toBe(true))
 expect(result.current.gridcraftNative).toBe(grid===true);expect(result.current.wordcraftNative).toBe(false);expect(result.current.data).toBe(false)
 unmount();client.clear()
})
it.each([403,503])('withdraws cached qualified Sheet capability on a failed %s refresh',async(status)=>{
 fetchMock.mockResolvedValue({ok:true,json:async()=>({gridcraft_native:true})})
 const {result,unmount,client}=mount();await waitFor(()=>expect(result.current.gridcraftNative).toBe(true))
 fetchMock.mockResolvedValue({ok:false,status})
 await act(async()=>{expect((await result.current.refetch()).isError).toBe(true)})
 await waitFor(()=>expect(result.current.gridcraftNative).toBe(false));unmount();client.clear()
})
