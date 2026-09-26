import React, { useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { usePageSnapshot, runAction, getActionStatus } from '@crewship/pages'
import './style.css'

type Narrative = { verdict?: string; blocks?: { text: string }[] }
type Table = { columns: { key: string; label: string }[]; rows: Record<string, string | number | boolean>[] }
const story = __STORY_CONFIG__
function App() {
 const page = usePageSnapshot()
 const panels = page?.panels ?? []
 const panel = (id: string) => panels.find(p => p.id === id)
 const finding = panel('finding')?.data as Narrative | undefined
 const outcome = panel('outcome')?.data as Narrative | undefined
 const draft = panel('draft')?.data as Narrative | undefined
 const tablePanel = panels.filter(p => ['records','resolved-records'].includes(p.id) && p.data).sort((a,b)=>(b.producedAt ?? '').localeCompare(a.producedAt ?? ''))[0]
 const table = tablePanel?.data as Table | undefined
 const [busy, setBusy] = useState(false)
 const [message, setMessage] = useState('')
 const [receipt, setReceipt] = useState('')
 const [runStatus, setRunStatus] = useState('')
 const key = useRef<{action: string; value: string} | null>(null)
 const completed = outcome?.verdict === 'Completed'
 const waiting = runStatus === 'waiting'
 useEffect(() => {
  if (!receipt) return
  let alive = true
  const poll = async () => {
   try {
    const result = await getActionStatus(receipt)
    if (!alive) return
    const status = result.run_status?.toLowerCase() || result.pending_status?.toLowerCase()
    setRunStatus(status)
    if (['completed','failed','cancelled','expired','interrupted'].includes(status)) {
     setBusy(false); setReceipt(''); key.current = null
     setMessage(status === 'completed' ? 'Run completed. The result is shown below.' : 'This run did not finish. Its details are available in Routines → History; you can retry.')
    } else if (status === 'waiting') setMessage('Your decision is ready in Inbox. Approve the demo action or choose Keep open.')
    else setMessage('Working on this project. The Page updates automatically.')
   } catch { if (alive) setMessage('Status is temporarily unavailable. Check Routines → History before starting another run.') }
  }
  void poll(); const timer = setInterval(poll,2000)
  return () => { alive=false; clearInterval(timer) }
 },[receipt])
 async function act(action: string) {
  if (busy) return
  setBusy(true); setRunStatus(''); setMessage('Confirm the action in Crewship.')
  // The resolve key survives a Page reload while approval is pending. A
  // recorded outcome (including Keep open) permits the next deliberate attempt.
  const version = panel(action === 'resolve' ? 'outcome' : action === 'draft' ? 'draft' : 'finding')?.producedAt || 'initial'
  if (key.current?.action !== action) key.current = {action, value: `demo-${story.slug}-${action}-${version}`}
  try {
   const result = await runAction('about',action,{}, {idempotencyKey:key.current.value})
   setReceipt(result.pending_id)
   setMessage('Run accepted. Waiting for its result…')
  } catch (error) {
   setBusy(false)
   setMessage(error instanceof Error ? error.message : 'The action could not start.')
   // A dismissed confirmation never submitted a write. A timeout retains its key.
   if (error instanceof Error && /cancel|dismiss/i.test(error.message)) key.current=null
  }
 }
 const result = panels.filter(p => ['outcome','finding'].includes(p.id) && p.data).sort((a,b)=>(b.producedAt ?? '').localeCompare(a.producedAt ?? ''))[0]?.data as Narrative | undefined
 return <main className="demo">
  <div className="topline"><span className="brand"><b>C</b><strong>HARBOR GOODS</strong><i>/</i>{story.project.toUpperCase()}</span><span className="tag">LOCAL DEMO DATA</span></div>
  <header className="hero"><div><div className="eyebrow">ONE SMALL TASK. A VISIBLE RESULT.</div><h1>{story.name}</h1><p>{story.problem}</p></div><div className="hero-signal"><span>PROJECT STATUS</span><strong>{busy ? (waiting ? 'Your decision' : 'Working…') : completed ? 'Completed' : finding?.verdict || 'Ready to start'}</strong><small>{completed ? 'Evidence saved in this workspace' : `${story.rows.length} sample records · ${story.agent.charAt(0).toUpperCase()+story.agent.slice(1)} is your agent`}</small><div className="signal-line" /></div></header>
  <section className="story">
   <div><span className="story-step">01 · CHECK</span><strong>{story.check_label}</strong><p>A local script checks the records and adds its findings to the prepared Issue.</p><button className="story-action" disabled={busy || waiting} onClick={()=>void act('check')}>{story.check_label}</button></div>
   <div><span className="story-step">02 · OPTIONAL AI</span><strong>Ask your agent</strong><p>Generate a fresh draft using your connected AI provider. A clearly labelled sample template is ready if you skip this step.</p><button className="story-action" disabled={busy || waiting || completed || !finding || finding.verdict==='No action needed'} onClick={()=>void act('draft')}>Draft with AI</button></div>
   <div><span className="story-step">03 · FINISH</span><strong>{story.resolve_label}</strong><p>{story.approval ? 'Review the proposal in Inbox. Approval saves a local artifact and completes the Issue.' : 'Repair the local demo delivery. Save its receipt and complete the Issue.'}</p><button className="story-action" disabled={busy || waiting || completed || !finding || finding.verdict==='No action needed'} onClick={()=>void act('resolve')}>{completed ? 'Completed' : waiting ? 'Review in Inbox' : story.resolve_label}</button></div>
  </section>
  {message && <p className="action-status" role="status">{message}</p>}
  {waiting && <p className="action-status">Open Inbox to decide on this project. Another run will not create a second approval.</p>}
  <section className="panels">
   <article className="panel"><span className="eyebrow">{table ? 'CHECKED RECORDS' : 'PREPARED SAMPLE'}</span><h2>{story.rows.length} records to inspect</h2><div className="table-wrap"><table><thead><tr>{(table?.columns || [{key:'id',label:'Record'},{key:'detail',label:'Sample data'}]).map(c=><th key={c.key}>{c.label}</th>)}</tr></thead><tbody>{table ? table.rows.map(row=><tr key={String(row.id)}>{table.columns.map(c=><td key={c.key}>{String(row[c.key] ?? '—')}</td>)}</tr>) : story.rows.map((row: Record<string, unknown>)=><tr key={String(row.id)}><td>{String(row.id)}</td><td>{Object.entries(row).filter(([k])=>k!=='id').map(([k,v])=>`${k.replaceAll('_',' ')}: ${v}`).join(' · ')}</td></tr>)}</tbody></table></div></article>
   <article className="panel"><span className="eyebrow">RESULT &amp; EVIDENCE</span><h2>{result?.verdict || 'Ready for the first check'}</h2><div className="narrative">{result?.blocks?.map((b,i)=><p key={i}>{b.text}</p>) || <p>Start the check above. It updates this Page and the Issue in the {story.project} project.</p>}</div></article>
   {draft?.verdict && <article className="panel"><span className="eyebrow">GENERATED BY YOUR AGENT</span><h2>{draft.verdict}</h2><div className="narrative">{draft.blocks?.map((b,i)=><p key={i}>{b.text}</p>)}</div></article>}
  </section>
  <aside className="source-note"><strong>How this example connects to your business</strong><p>This example uses fictional records. Replace the sample source with {story.source}. The same flow can check the data, track the work in Issues and ask you for a decision in Inbox.</p><details><summary>Inspect the example</summary><p>Data and scripts: /crew/shared/demo/business/. Local evidence: outbox/{story.slug}.json. The related Issue is “{story.issue_title}”. Each action runs a named routine with its own history. Only one action per project runs at a time.</p></details></aside>
  <footer><span>CREWSHIP DEMO · {story.project.toUpperCase()}</span><span>No real customer is contacted</span></footer>
 </main>
}
createRoot(document.getElementById('root')!).render(<App />)
