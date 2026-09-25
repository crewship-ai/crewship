// Bounded Linux cgroup-v2 sample from Ops plus live fleet metrics through its
// local, workspace-bound sidecar. Missing metrics remain unknown, never zero.
import { readFile } from 'node:fs/promises'
const read = async path => (await readFile(path, 'utf8')).trim()
const memory = async () => Number(await read('/sys/fs/cgroup/memory.current'))
const cpu = async () => {
  const row = (await read('/sys/fs/cgroup/cpu.stat')).split('\n').find(x => x.startsWith('usage_usec '))
  if (!row) throw new Error('cgroup CPU accounting unavailable')
  return Number(row.split(/\s+/)[1])
}
const fleetSample = async () => {
  try {
    const response = await fetch('http://127.0.0.1:9119/crews/telemetry', {signal: AbortSignal.timeout(8000)})
    if (!response.ok) return null
    const value = await response.json()
    return Array.isArray(value?.crews) ? value.crews : null
  } catch { return null }
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
  const fleet = await fleetSample()
  const columns = [
    {key:'crew',label:'Crew'}, {key:'container',label:'Container'},
    {key:'status',label:'Status'}, {key:'cpu',label:'CPU'}, {key:'memory',label:'Memory'},
  ]
  const rows = (fleet ?? []).flatMap(crew => {
    const containers = Array.isArray(crew.containers) ? crew.containers : []
    if (!containers.length) return [{crew:crew.name, container:null, status:crew.available ? 'No container' : 'Unavailable', cpu:null, memory:null}]
    return containers.map(container => ({
      crew:crew.name,
      container:container.name,
      status:container.status,
      cpu:Number.isFinite(container.cpu_percent) ? `${container.cpu_percent.toFixed(1)}%` : null,
      memory:Number.isFinite(container.memory_mb) ? `${container.memory_mb} MB` : null,
    }))
  }).slice(0, 100)
  const completeFleet = fleet !== null && fleet.every(crew => crew.available)
  const runningContainers = completeFleet ? fleet.reduce((count, crew) => count + crew.containers.filter(container => container.status === 'running').length, 0) : null
  console.log(JSON.stringify({
    services: {items: [
      {name:'Crews in workspace', state:fleet ? 'ok':'warning', label:fleet ? String(fleet.length) : 'Unavailable'},
      {name:'Running containers', state:completeFleet ? 'ok':'warning', label:runningContainers === null ? 'Unavailable' : String(runningContainers)},
      {name:'Ops memory used', state:limitMB && used/limitMB > .85 ? 'warning':'ok', label:`${used} MB`},
      {name:'Ops memory limit', state:limitMB ? 'ok':'warning', label:limitMB ? `${limitMB} MB` : 'Unlimited'},
      {name:'Ops CPU used', state:cores && usedCores/cores > .85 ? 'warning':'ok', label:`${usedCores.toFixed(2)} / ${cores ?? 'unlimited'} cores`},
    ]},
    memory:{value:used,unit:'MB',sparkline:samples},
    fleet:{columns,rows},
  }))
} catch {
  // Never mark a failed/missing measurement green and never expose diagnostics.
  console.error('Container resource sample failed; retaining the previous Page snapshot.')
  process.exitCode = 1
}
