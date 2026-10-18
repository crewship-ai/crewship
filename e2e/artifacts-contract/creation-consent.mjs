import assert from 'node:assert/strict'

/** Observe the production composer's explicit consent, never infer it from English. */
export function creationConsent(frame,chatId,prompt) {
  try {
    const outer=JSON.parse(String(frame))
    if(outer.type!=='send_message' || outer.channel!==`session:${chatId}`) return null
    const body=typeof outer.payload==='string'?JSON.parse(outer.payload):outer.payload
    const intent=body?.metadata?.document_creation_intent
    if(body?.session_id!==chatId || body.content!==prompt || intent?.drafting!==true ||
      typeof intent.submission_id!=='string' || !/^[A-Za-z0-9_-]{1,128}$/.test(intent.submission_id)) return null
    return {submission_id:intent.submission_id,drafting:true}
  } catch {return null}
}

export async function submitDocumentCreation(page,config) {
  const prompt=`Create one populated private Document titled ${JSON.stringify(config.title)} containing exactly this text: ${JSON.stringify(config.initialText)}. Use the supported document creation tool. Do not create any other resource.`
  let intent
  const watched=[]
  const watch=socket=>{
    const sent=event=>{intent ??= creationConsent(event.payload,config.chatId,prompt)}
    socket.on('framesent',sent);watched.push([socket,sent])
  }
  page.on('websocket',watch)
  try {
    await page.goto(new URL(config.chatPath,config.server).href)
    const control=page.getByRole('button',{name:'Create document',exact:true})
    await control.click()
    assert.equal(await control.getAttribute('aria-pressed'),'true','explicit creation control is selected')
    const composer=page.locator('textarea:visible')
    assert.equal(await composer.count(),1,'one visible composer on the dedicated chat page')
    await composer.fill(prompt)
    await composer.press('Enter')
    const deadline=Date.now()+30_000
    while(!intent && Date.now()<deadline) await new Promise(resolve=>setTimeout(resolve,100))
    assert.ok(intent,'real composer must send explicit creation intent on the expected chat frame')
    return intent
  } finally {
    page.off('websocket',watch)
    for(const [socket,sent] of watched) socket.off('framesent',sent)
  }
}
