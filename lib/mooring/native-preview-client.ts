import {apiFetch} from '../api-fetch'
import {validNativeVersion} from './native-draft'
import type {NativeDocumentSnapshot} from './native-document-client'

export type NativePreviewSnapshot=Omit<NativeDocumentSnapshot,'quota'>
export class NativePreviewError extends Error {
 constructor(readonly status:number){super('native_shared_preview_unavailable')}
}
const identifier=(value:string)=>value.length>0&&value.length<=256&&!value.includes('\0')

/** This read has ordinary human auth only. It never opens an admission or
 * retries against the latest document when a published pin is unavailable. */
export async function readNativePreview(scope:{workspaceId:string;pageId:string},signal:AbortSignal,transport:typeof fetch=apiFetch):Promise<NativePreviewSnapshot>{
 if(!identifier(scope.workspaceId)||!identifier(scope.pageId)||signal.aborted)throw new NativePreviewError(0)
 const query=new URLSearchParams({workspace_id:scope.workspaceId})
 const response=await transport(`/api/v1/pages/${encodeURIComponent('@'+scope.pageId)}/craft/preview?${query}`,{method:'GET',cache:'no-store',redirect:'error',signal})
 if(signal.aborted||!response.ok)throw new NativePreviewError(response.status)
 const raw:unknown=await response.json()
 if(signal.aborted||!raw||typeof raw!=='object'||Array.isArray(raw))throw new NativePreviewError(0)
 const v=raw as Record<string,unknown>
 if(!validNativeVersion(v.version)||v.version==='0'||v.applicationId!=='wordcraft'&&v.applicationId!=='gridcraft'||typeof v.enginePin!=='string'||!/^[a-f0-9]{64}$/.test(v.enginePin)||typeof v.editorBundle!=='string'||!/^[a-f0-9]{64}$/.test(v.editorBundle)||typeof v.editorURL!=='string'||!v.editorURL.endsWith(`/b/${v.editorBundle}/index.html`)||typeof v.bytes!=='string'||!v.bytes.length||v.bytes.length>44739244||v.bytes.length%4!==0)throw new NativePreviewError(0)
 let bytes:Uint8Array
 try{
  const binary=atob(v.bytes)
  if(!binary.length||binary.length>32*1024*1024||btoa(binary)!==v.bytes)throw Error('invalid_base64')
  bytes=Uint8Array.from(binary,c=>c.charCodeAt(0))
 }catch{throw new NativePreviewError(0)}
 return {version:v.version,bytes,applicationId:v.applicationId,enginePin:v.enginePin,editorBundle:v.editorBundle,editorURL:v.editorURL}
}
