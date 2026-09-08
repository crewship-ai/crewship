import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FilePreview } from './file-preview'
import { readPreviewBytes } from './file-preview-data'
vi.mock('./file-preview-data', async original => ({...await original<typeof import('./file-preview-data')>(),readPreviewBytes:vi.fn()}))
const pdf = vi.hoisted(() => ({destroy:vi.fn(),cancel:vi.fn(),getPage:vi.fn(),getDocument:vi.fn(),worker:{workerSrc:''}}))
vi.mock('pdfjs-dist', () => ({GlobalWorkerOptions:pdf.worker,getDocument:pdf.getDocument}))
const read=vi.mocked(readPreviewBytes)
const png=new Uint8Array([137,80,78,71,13,10,26,10])
const pdfBytes=new Uint8Array([37,80,68,70,45])
beforeEach(()=>{
 vi.clearAllMocks()
 vi.stubGlobal('URL',Object.assign(URL,{createObjectURL:vi.fn(()=> 'blob:preview'),revokeObjectURL:vi.fn()}))
 pdf.getPage.mockResolvedValue({getViewport:({scale}:{scale:number})=>({width:300*scale,height:400*scale}),render:()=>({promise:Promise.resolve(),cancel:pdf.cancel})})
 pdf.getDocument.mockReturnValue({promise:Promise.resolve({numPages:2,getPage:pdf.getPage}),destroy:pdf.destroy})
})
afterEach(()=>{cleanup();vi.unstubAllGlobals()})
describe('FilePreview',()=>{
 it('renders raster bytes, revokes old URLs, and aborts when switched',async()=>{
  read.mockResolvedValue(png)
  const {rerender,unmount}=render(<FilePreview url="/api/one" name="one.png" onClose={()=>{}} />)
  await screen.findByAltText('one.png')
  const firstSignal=read.mock.calls[0][1]
  rerender(<FilePreview url="/api/two" name="two.png" onClose={()=>{}} />)
  await screen.findByAltText('two.png')
  expect(firstSignal.aborted).toBe(true);expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview')
  unmount();expect(read.mock.calls[1][1].aborted).toBe(true)
 })
 it('ignores a stale response arriving after file selection changes',async()=>{
  let resolve!:(bytes:Uint8Array<ArrayBuffer>)=>void
  read.mockReturnValueOnce(new Promise(r=>{resolve=r})).mockResolvedValueOnce(png)
  const {rerender}=render(<FilePreview url="/api/old" name="old.pdf" onClose={()=>{}} />)
  rerender(<FilePreview url="/api/new" name="new.png" onClose={()=>{}} />)
  await screen.findByAltText('new.png');await act(async()=>resolve(pdfBytes))
  expect(pdf.getDocument).not.toHaveBeenCalled();expect(screen.queryByText('old.pdf')).not.toBeInTheDocument()
 })
 it('does not render disguised HTML or SVG and offers a download',async()=>{
  read.mockResolvedValue(new TextEncoder().encode('<svg onload="alert(1)"></svg>'))
  render(<FilePreview url="/api/file" name="claimed.png" onClose={()=>{}} />)
  await screen.findByText(/Preview is available/)
  expect(screen.queryByRole('img')).not.toBeInTheDocument();expect(document.querySelector('iframe')).toBeNull()
  expect(screen.getByRole('button',{name:'Download file'})).toBeEnabled()
 })
 it('retries failed fetch and exposes close action',async()=>{
  read.mockRejectedValueOnce(new Error('403')).mockResolvedValueOnce(png)
  const close=vi.fn();render(<FilePreview url="/api/file" name="a.png" onClose={close} />)
  await screen.findByRole('alert');fireEvent.click(screen.getByRole('button',{name:'Retry preview'}))
  await screen.findByAltText('a.png');fireEvent.click(screen.getByRole('button',{name:'Back to files'}));expect(close).toHaveBeenCalledOnce()
 })
 it('renders PDF pages locally with page navigation and bounded zoom; tears down worker',async()=>{
  read.mockResolvedValue(pdfBytes)
  const {unmount}=render(<FilePreview url="/api/file" name="a.pdf" onClose={()=>{}} />)
  await screen.findByText('Page 1 of 2');await waitFor(()=>expect(screen.queryByText('Rendering PDF…')).not.toBeInTheDocument())
  expect(pdf.worker.workerSrc).toBe('/pdfjs/pdf.worker.min.mjs')
  expect(pdf.getDocument).toHaveBeenCalledWith(expect.objectContaining({enableXfa:false,maxImageSize:16_777_216}))
  fireEvent.click(screen.getByRole('button',{name:'Next page'}));await screen.findByText('Page 2 of 2');expect(screen.getByRole('button',{name:'Next page'})).toBeDisabled()
  fireEvent.click(screen.getByRole('button',{name:'Zoom in'}));await screen.findByText('125%')
  await waitFor(()=>expect(pdf.getPage).toHaveBeenCalledWith(2));unmount();expect(pdf.destroy).toHaveBeenCalled()
 })
 it('offers a retry when PDF parser rejects an invalid document',async()=>{
  read.mockResolvedValue(pdfBytes)
  pdf.getDocument.mockImplementationOnce(()=>({promise:Promise.reject(new Error('Invalid PDF')),destroy:pdf.destroy}))
  render(<FilePreview url="/api/file" name="a.pdf" onClose={()=>{}} />)
  await screen.findByText(/This PDF could not be opened/)
  fireEvent.click(screen.getByRole('button',{name:'Retry PDF preview'}));await screen.findByText('Page 1 of 2')
 })
 it('terminates password requests and explains the download fallback',async()=>{
  read.mockResolvedValue(pdfBytes)
  const loading={promise:new Promise(()=>{}),destroy:pdf.destroy,onPassword:undefined as undefined|(()=>void)}
  pdf.getDocument.mockReturnValueOnce(loading)
  render(<FilePreview url="/api/file" name="locked.pdf" onClose={()=>{}} />)
  await waitFor(()=>expect(loading.onPassword).toBeTypeOf('function'))
  act(()=>loading.onPassword?.())
  await screen.findByText(/This PDF is password protected/);expect(pdf.destroy).toHaveBeenCalled()
 })
 it('refits the PDF when its actual panel width changes',async()=>{
  let resize!:(entries:{contentRect:{width:number}}[])=>void
  const disconnect=vi.fn()
  vi.stubGlobal('ResizeObserver',class { constructor(callback:typeof resize){resize=callback} observe(){} disconnect=disconnect })
  read.mockResolvedValue(pdfBytes)
  const {unmount}=render(<FilePreview url="/api/file" name="a.pdf" onClose={()=>{}} />)
  await screen.findByText('Page 1 of 2');await waitFor(()=>expect(screen.queryByText('Rendering PDF…')).not.toBeInTheDocument())
  act(()=>resize([{contentRect:{width:250}}]))
  await waitFor(()=>expect(screen.getByLabelText('PDF page 1').style.width).toBe('226px'))
  unmount();expect(disconnect).toHaveBeenCalled()
 })

})
