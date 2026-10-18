import { writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { createPrivateArtifacts,readPrivateJson } from '../helpers/private-artifacts.mjs'
import { runContract,validateConfig } from './contract.mjs'
import { documentAdapter } from './document.mjs'
const filename=process.argv[2]
if(!filename) throw Error('Usage: node e2e/artifacts-contract/run.mjs PRIVATE_CONFIG.json')
const config=await readPrivateJson(filename)
validateConfig(config)
const output=await createPrivateArtifacts('artifacts-contract')
let browser,adapter={}
try {
  if(config.type==='document' && config.variant==='manual-sonnet-5.5') {
    const {chromium}=await import('playwright')
    browser=await chromium.launch({headless:true})
    adapter=await documentAdapter(config,browser)
  }
  if(config.variant==='deterministic-fixture') {
    adapter.agentCreate=async()=>({status:'incomplete',reason:'Deterministic provider fixture is not wired; no provider or agent was invoked'})
  }
  const report=await runContract(config,adapter)
  await writeFile(join(output,'report.json'),JSON.stringify(report,null,2)+'\n',{flag:'wx',mode:0o600})
  console.log(`${report.variant}: ${report.status}; liveAcceptance=false`)
  process.exitCode=report.status==='passed'?0:report.status==='incomplete'?2:1
} catch {
  await writeFile(join(output,'report.json'),JSON.stringify({schema:1,type:config.type,variant:config.variant,status:'failed',liveAcceptance:false,reason:'setup failed; no acceptance evidence'})+'\n',{flag:'wx',mode:0o600})
  console.error('Setup failed; inspect private configuration and dedicated target. No acceptance evidence.')
  process.exitCode=1
} finally {
  try {await adapter.close?.()} finally {await browser?.close()}
}
