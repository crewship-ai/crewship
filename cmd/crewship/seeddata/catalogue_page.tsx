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
  const [pending, setPending] = useState(false)
  const status = panel.state === 'fresh' ? 'Live' : panel.state === 'stale' ? 'Needs refresh' : 'Waiting for data'
  async function showHistory() {
    try { const result = await getPanelHistory(panel.id, { limit: 10 }); setHistory(`${result.items.length} recorded snapshots`) }
    catch { setHistory('History is unavailable') }
  }
  async function act(actionId: string) {
    setPending(true)
    try { await runAction(panel.id, actionId); setHistory('Routine queued. This card updates when its run finishes.') }
    catch { setHistory('The routine could not start. Check its run history.') }
    finally { setPending(false) }
  }
  const actions = ((panel as unknown as { actions?: { id: string; label: string }[] }).actions ?? [])
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
    <div className="panel-foot"><span>{panel.producedAt ? `Updated ${new Date(panel.producedAt).toLocaleString('en-US')}` : 'No snapshot yet'}</span><div><button onClick={showHistory}>History</button>{actions.map(a => <button className="action" disabled={pending} key={a.id} onClick={() => act(a.id)}>{a.label}</button>)}</div></div>
    {history && <p className="feedback" role="status">{history}</p>}
  </article>
}

function App() {
  const page = usePageSnapshot()
  const panels = page?.panels ?? []
  const fresh = panels.filter(p => p.state === 'fresh').length
  return <main className="demo">
    <div className="topline"><span className="brand"><b>C</b><strong>CREWSHIP</strong><i>/</i> DEMO</span><span className="tag">LIVE WORKSPACE PAGE</span></div>
    <header className="hero"><div><div className="eyebrow">A WORKFLOW YOU CAN INSPECT</div><h1>{page?.name ?? 'Connecting to Crewship'}</h1><p>Explore snapshots produced by the routines and agents in this workspace. Open a panel to inspect its history or run its available action.</p></div><div className="hero-signal"><span>PAGE STATUS</span><strong>{fresh === panels.length && panels.length ? 'Up to date' : 'Ready to explore'}</strong><small>{fresh} of {panels.length} panels have a fresh snapshot</small><div className="signal-line" /></div></header>
    <nav className="toolbar"><strong>Overview</strong><span>{panels.length} panels · {fresh} fresh</span></nav>
    <section className="panels">{panels.map(panel => <Panel key={panel.id} panel={panel} />)}</section>
    <footer><span>CREWSHIP PAGES <b>×</b> WORKSPACE DATA</span><span>Snapshots come from governed producers</span></footer>
  </main>
}

createRoot(document.getElementById('root')!).render(<App />)
