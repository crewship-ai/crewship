"use client"
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog,DialogContent,DialogHeader,DialogTitle,DialogDescription,DialogFooter } from '@/components/ui/dialog'
import type { ArtifactCopyInput } from '@/hooks/use-artifact-copy'
export function ArtifactCopyDialog({ revision,pending,uncertain,error,onCopy,onClose }: {
 revision:number;pending:boolean;uncertain:boolean;error?:string;onCopy:(input:ArtifactCopyInput)=>void;onClose:()=>void
}) {
 const [name,setName]=useState(''),[slug,setSlug]=useState('')
 const [intent,setIntent]=useState<ArtifactCopyInput|null>(null)
 const locked=pending||uncertain
 return <Dialog open onOpenChange={open=>{if(!open&&!locked)onClose()}}><DialogContent><DialogHeader><DialogTitle>Copy artifact</DialogTitle><DialogDescription>Copy revision {revision} to a new page owned by you. Sharing, comments and agent permissions are not copied. The published HTML stays unchanged.</DialogDescription></DialogHeader>
 <form className="space-y-4" onSubmit={event=>{event.preventDefault();const input=locked&&intent?intent:{source_revision:revision,name:name.trim(),slug};setIntent(input);onCopy(input)}}>
 <div className="space-y-2"><label htmlFor="copy-artifact-name">Copy name</label><Input id="copy-artifact-name" autoFocus required maxLength={200} value={name} disabled={locked} onChange={event=>setName(event.target.value)}/></div>
 <div className="space-y-2"><label htmlFor="copy-artifact-slug">Copy page address</label><Input id="copy-artifact-slug" required maxLength={80} pattern="[a-z0-9]+(-[a-z0-9]+)*" value={slug} disabled={locked} onChange={event=>setSlug(event.target.value)}/></div>
 {error&&<p role="alert" className="text-sm text-destructive">{error}</p>}
 {uncertain&&<p className="text-sm text-muted-foreground">The copy may already exist. Retry checks the same operation.</p>}
 <DialogFooter><Button type="button" variant="ghost" className="coarse:h-12" disabled={locked} onClick={onClose}>Cancel</Button><Button type="submit" className="coarse:h-12" disabled={pending||!name.trim()||!slug}>{pending?'Copying…':uncertain?'Retry same copy':'Create private copy'}</Button></DialogFooter>
 </form></DialogContent></Dialog>
}
