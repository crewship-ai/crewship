import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { usePageSnapshot, getPanelHistory, runAction } from '@crewship/pages'
import './style.css'

type Item = { name?: string; label?: string; state?: string }
type Block = { kind?: string; text?: string }
type Cell = string | number | null
type Data = { items?: Item[]; value?: number; unit?: string; sparkline?: number[]; columns?: { key: string; label: string }[]; rows?: Record<string, Cell>[]; verdict?: string; blocks?: Block[]; points?: { value: number }[] }

function Panel({ panel }: { panel: NonNullable<ReturnType<typeof usePageSnapshot>>['panels'][number] }) {
  const data = (panel.data ?? {}) as Data
  const [history, setHistory] = useState('')
  const status = panel.state === 'fresh' ? 'Fresh snapshot' : panel.state === 'stale' ? 'Needs refresh' : 'Waiting for data'
  async function showHistory() {
    try { const result = await getPanelHistory(panel.id, { limit: 10 }); setHistory(`${result.items.length} recorded snapshots`) }
    catch { setHistory('History is unavailable') }
  }
  return <article className="panel">
    <div className="panel-head"><span className="eyebrow">{panel.schema?.replace('.v1', '').toUpperCase() ?? 'PANEL'}</span><span className={`state ${panel.state === 'fresh' ? 'fresh' : ''}`}><i />{status}</span></div>
    <h2>{panel.title || panel.id}</h2>
    {data.items && <div className="status-grid">{data.items.map((item, i) => <div className="status-item" key={`${item.name}-${i}`}><span>{item.name}</span><strong>{item.label || item.state || '—'}</strong><small className={item.state === 'ok' ? 'good' : 'attention'}>{item.state || 'unknown'}</small></div>)}</div>}
    {typeof data.value === 'number' && <div className="big-value">{data.value}<small>{data.unit}</small></div>}
    {data.sparkline && <div className="bars" aria-label="Recent measurements">{data.sparkline.map((v, i) => <i key={i} style={{height: `${Math.max(18, (v / Math.max(...data.sparkline!, 1)) * 100)}%`}} />)}</div>}
    {data.points && <div className="bars" aria-label="Recent measurements">{data.points.map((point, i) => <i key={i} style={{height: `${Math.max(18, (point.value / Math.max(...data.points!.map(p => p.value), 1)) * 100)}%`}} />)}</div>}
    {data.verdict && <div className="verdict">{data.verdict}</div>}
    {data.blocks && <div className="narrative">{data.blocks.map((block, i) => <p key={i}>{block.text}</p>)}</div>}
    {data.columns && <div className="table-wrap"><table><thead><tr>{data.columns.map(c => <th key={c.key}>{c.label}</th>)}</tr></thead><tbody>{(data.rows ?? []).map((row, i) => <tr key={i}>{data.columns!.map(c => <td key={c.key}>{row[c.key] ?? '—'}</td>)}</tr>)}</tbody></table></div>}
    {!data.items && typeof data.value !== 'number' && !data.verdict && !data.columns && !data.points && <p className="empty">This panel is ready for its first producer run.</p>}
    <div className="panel-foot"><span>{panel.producedAt ? `Updated ${new Date(panel.producedAt).toLocaleString('en-US')}` : 'No snapshot yet'}</span><div><button onClick={showHistory}>History</button></div></div>
    {history && <p className="feedback" role="status">{history}</p>}
  </article>
}

function App() {
  const page = usePageSnapshot()
  const panels = page?.panels ?? []
  const fresh = panels.filter(p => p.state === 'fresh').length
  const leadStory = page?.name === 'Leads at Risk'
  const [checking, setChecking] = useState(false)
  const [checkFeedback, setCheckFeedback] = useState('')
  async function checkInquiries() {
    setChecking(true)
    try { await runAction('inquiries', 'check'); setCheckFeedback('Check queued. Watch the finding below, then open Inbox to review the draft.') }
    catch { setCheckFeedback('The check could not start. Open Routines to see the reason.') }
    finally { setChecking(false) }
  }
  return <main className="demo">
    <div className="topline"><span className="brand"><b>C</b><strong>CREWSHIP</strong><i>/</i> DEMO</span><span className="tag">WORKSPACE PAGE</span></div>
    <header className="hero"><div><div className="eyebrow">A WORKFLOW YOU CAN INSPECT</div><h1>{page?.name ?? 'Connecting to Crewship'}</h1><p>{__PAGE_DESCRIPTION__}</p></div><div className="hero-signal"><span>PAGE STATUS</span><strong>{fresh === panels.length && panels.length ? 'Snapshots ready' : 'Ready to explore'}</strong><small>{fresh} of {panels.length} panels have a fresh snapshot</small><div className="signal-line" /></div></header>
    {leadStory && <section className="story" aria-label="How this demo works">
      <div><span className="story-step">01 · THE PROBLEM</span><strong>A customer may be lost</strong><p>Ava asked for a quote. Her inquiry is still unanswered after 26 sample hours.</p></div>
      <div><span className="story-step">02 · TRY IT</span><strong>Check the inquiries</strong><p>Run a real Crewship routine against three sample records.</p><button className="story-action" onClick={checkInquiries} disabled={checking}>{checking ? 'Starting…' : 'Check inquiries'}</button>{checkFeedback && <p className="story-feedback" role="status">{checkFeedback}</p>}</div>
      <div><span className="story-step">03 · YOUR DECISION</span><strong>Review the proposed reply</strong><p>The finding appears here. A draft arrives in Inbox for your approval; no email is sent.</p></div>
    </section>}
    <nav className="toolbar"><strong>Overview</strong><span>{panels.length} panels · {fresh} fresh</span></nav>
    <section className="panels">{panels.map(panel => <Panel key={panel.id} panel={panel} />)}</section>
    {leadStory && <aside className="source-note"><strong>Where does the data come from?</strong><p>This demo reads <code>/crew/shared/demo/harbor-goods/leads.json</code> inside the Ops crew. Your workspace could use a connected Gmail account, Google Sheet, CRM or form instead. A calendar could schedule the follow-up. No live account is connected to this demo.</p></aside>}
    <footer><span>CREWSHIP PAGES <b>×</b> WORKSPACE DATA</span><span>Snapshots come from governed producers</span></footer>
  </main>
}

createRoot(document.getElementById('root')!).render(<App />)
