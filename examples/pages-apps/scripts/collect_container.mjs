// Bounded Linux cgroup-v2 sample. Runs in the author crew container; no network,
// child process, credentials, LLM call or host filesystem access is required.
import { readFile } from 'node:fs/promises'
const read = async path => (await readFile(path, 'utf8')).trim()
const memory = async () => Number(await read('/sys/fs/cgroup/memory.current'))
const cpu = async () => {
  const row = (await read('/sys/fs/cgroup/cpu.stat')).split('\n').find(x => x.startsWith('usage_usec '))
  if (!row) throw new Error('cgroup CPU accounting unavailable')
  return Number(row.split(/\s+/)[1])
}
try {
  const limitText = await read('/sys/fs/cgroup/memory.max')
  const limit = limitText === 'max' ? null : Number(limitText)
  const [quotaText, periodText] = (await read('/sys/fs/cgroup/cpu.max')).split(/\s+/)
  const cores = quotaText === 'max' ? null : Number(quotaText)/Number(periodText)
  if (limit !== null && (!Number.isFinite(limit) || limit <= 0)) throw new Error('invalid cgroup memory limit')
  if (!Number.isFinite(Number(periodText)) || Number(periodText) <= 0 ||
      (cores !== null && (!Number.isFinite(cores) || cores <= 0))) throw new Error('invalid cgroup CPU limit')
  const before = await cpu()
  const start = process.hrtime.bigint()
  const samples = []
  for (let i = 0; i < 8; i++) {
    samples.push(Math.round((await memory())/1024/1024))
    await new Promise(resolve => setTimeout(resolve, 150))
  }
  const durationUsec = Number(process.hrtime.bigint() - start)/1000
  const usedCores = Math.max(0, (await cpu() - before)/durationUsec)
  const used = samples.at(-1)
  const limitMB = limit == null ? null : Math.round(limit/1024/1024)
  if (![used, usedCores, ...samples].every(Number.isFinite)) throw new Error('invalid cgroup sample')
  console.log(JSON.stringify({
    services: {items: [
      {name:'Kontejner Ops', state:'ok', label:'Kolektor dokončen'},
      {name:'Paměť kontejneru', state:limitMB && used/limitMB > .85 ? 'warning':'ok', label:`${used} MB využito`},
      {name:'Limit paměti', state:limitMB ? 'ok':'warning', label:limitMB ? `${limitMB} MB` : 'Nenastaven'},
      {name:'CPU kontejneru', state:cores && usedCores/cores > .85 ? 'warning':'ok', label:`${usedCores.toFixed(2)} / ${cores ?? 'bez limitu'} CPU`},
    ]},
    memory:{value:used,unit:'MB',sparkline:samples},
  }))
} catch {
  // Never mark a failed/missing measurement green and never expose diagnostics.
  console.error('Container resource sample failed; retaining the previous Page snapshot.')
  process.exitCode = 1
}
