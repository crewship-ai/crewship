"use client"

import { useState } from "react"
import Link from "next/link"
import { UserRoundCheck } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel } from "@/components/ui/alert-dialog"
import { usePageOwnership } from "@/hooks/use-page-ownership"
import { PageOwnershipError, type PageOwnership } from "@/lib/page-ownership"
import { SettingsCard, nativeSelect } from "../shared"

export function PageOwnershipCard({ workspaceId, role }: { workspaceId: string; role: string | null }) {
  const [owner, setOwner] = useState("")
  const ownership = usePageOwnership(workspaceId, role, owner)
  const owners = [...new Map(ownership.data?.map(page => [page.owner_user_id, page.owner_name]) ?? []).entries()]
  if (owner && !owners.some(([id]) => id === owner)) owners.push([owner, owner])
  if (!ownership.canManage) return null
  return <SettingsCard title="Artifact and Board ownership" icon={UserRoundCheck} description="Transfer human-owned outputs explicitly before removing a member or deleting an account.">
    <div className="space-y-3 p-4">
      <p className="text-sm text-muted-foreground">A transfer takes effect immediately. The new owner gains control of the private output and its retained history. Content and history stay with the Page. Existing grants are rechecked under current permissions; the new owner may need to review sharing. Linked sources keep separate access checks. After transferring every affected output, retry the departure.</p>
      <Button variant="outline" className="coarse:h-12" disabled={ownership.query.isFetching || ownership.transfer.isPending} onClick={ownership.reviewLatest}>Refresh ownership</Button>
      {ownership.error && <p role="alert" className="text-sm text-destructive">{ownership.error.message}</p>}
      {ownership.data ? <>
        <label className="flex flex-col gap-2 text-sm md:flex-row md:items-center">Filter by owner
          <select className={`${nativeSelect} w-full md:w-64`} value={owner} disabled={ownership.transfer.isPending} onChange={event => { setOwner(event.target.value); ownership.transfer.reset() }}>
            <option value="">All owners</option>
            {owners.map(([id, name]) => <option key={id} value={id}>{name || id}</option>)}
          </select>
        </label>
        <p className="text-sm text-muted-foreground">{ownership.data.length} outputs loaded. The owner selector includes owners in these results.{ownership.query.hasNextPage ? " More outputs are available below." : ""}</p>
        <OwnershipList key={ownership.identity} pages={ownership.data} transfer={ownership.transfer} />
        {ownership.query.hasNextPage && <Button variant="outline" className="coarse:h-12" disabled={ownership.query.isFetching || ownership.transfer.isPending} onClick={() => void ownership.query.fetchNextPage()}>{ownership.query.isFetchingNextPage ? "Loading more…" : "Load more outputs"}</Button>}
      </> : !ownership.error && <Skeleton className="h-20" />}
    </div>
  </SettingsCard>
}

function OwnershipList({ pages, transfer }: { pages: PageOwnership[]; transfer: ReturnType<typeof usePageOwnership>["transfer"] }) {
  const [successors, setSuccessors] = useState<Record<string, string>>({})
  // Freeze the reviewed owner and successor together. Background reads never
  // advance the expected owner of an already-open confirmation.
  const [intent, setIntent] = useState<{ page: PageOwnership; successor: PageOwnership["eligible_successors"][number] } | null>(null)
  async function confirm() {
    if (!intent || transfer.isPending) return
    try {
      await transfer.mutateAsync({ pageId: intent.page.id, expected_owner_user_id: intent.page.owner_user_id, successor_user_id: intent.successor.user_id })
    } catch { /* The mutation exposes the error; conflict refresh never retries. */ }
    finally { setSuccessors(previous => { const next = { ...previous }; delete next[intent.page.id]; return next }); setIntent(null) }
  }
  return <>
    {transfer.error && <p role="alert" className="text-sm text-destructive">{transfer.error instanceof PageOwnershipError && transfer.error.status === 409 ? "Ownership changed. Review the refreshed owner and choose a successor again. " : ""}{transfer.error.message}</p>}
    {transfer.isSuccess && <p role="status" className="text-sm">Ownership transferred. Review any remaining outputs before retrying the departure.</p>}
    {!pages.length && <p className="text-sm text-muted-foreground">No Artifacts or Boards for this owner.</p>}
    <ul className="divide-y" aria-label="Artifact and Board ownership">
      {pages.map(page => {
        const successor = page.eligible_successors.find(candidate => candidate.user_id === successors[page.id])
        return <li key={`${page.id}:${page.owner_user_id}`} className="space-y-3 py-4">
          <div className="min-w-0 text-sm"><Link className="break-words font-medium underline underline-offset-4" href={`/pages/${encodeURIComponent(page.slug)}`}>{page.name || page.slug}</Link><p className="break-words text-muted-foreground">{page.type === "artifact" ? "Artifact" : "Board"} · Owner: {page.owner_name || page.owner_user_id}</p></div>
          {page.eligible_successors.length ? <div className="flex flex-col gap-2 md:flex-row md:items-end">
            <label className="flex min-w-0 flex-1 flex-col gap-2 text-sm">New owner for {page.name || page.slug}
              <select className={`${nativeSelect} w-full`} value={successor?.user_id ?? ""} disabled={transfer.isPending} onChange={event => { setSuccessors(previous => ({ ...previous, [page.id]: event.target.value })); transfer.reset() }}>
                <option value="">Choose an existing reader</option>
                {page.eligible_successors.map(candidate => <option key={candidate.user_id} value={candidate.user_id}>{candidate.name || candidate.email || candidate.user_id}{candidate.name && candidate.email ? ` (${candidate.email})` : ""}</option>)}
              </select>
            </label>
            <Button variant="outline" className="coarse:h-12" disabled={!successor || transfer.isPending} onClick={() => { transfer.reset(); setIntent({ page, successor: successor! }) }}>Transfer ownership of {page.name || page.slug}</Button>
          </div> : <p className="text-sm text-muted-foreground">No eligible successor currently has read access. Ask the current owner to open this Page&apos;s sharing settings and share it with the intended successor, then refresh ownership here.</p>}
        </li>
      })}
    </ul>
    <AlertDialog open={Boolean(intent)} onOpenChange={open => { if (!open && !transfer.isPending) setIntent(null) }}>
      <AlertDialogContent>
        <AlertDialogHeader><AlertDialogTitle>Transfer ownership of {intent?.page.name || intent?.page.slug}?</AlertDialogTitle>
          <AlertDialogDescription>{intent?.successor.name || intent?.successor.email} will replace {intent?.page.owner_name || intent?.page.owner_user_id} as owner immediately, gaining control of this private output and its retained history. Content and history stay with the Page. Existing grants are rechecked under current permissions; the new owner may need to review sharing. Linked sources keep separate access checks. This does not remove anyone from the workspace.</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter><AlertDialogCancel disabled={transfer.isPending}>Cancel</AlertDialogCancel><Button className="coarse:h-12" disabled={transfer.isPending} onClick={() => void confirm()}>{transfer.isPending ? "Transferring…" : "Confirm ownership transfer"}</Button></AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </>
}
