// Production frontend + Router + synthetic migrated SQLite, no route mocks.
// Called only by TestArtifactProjectBrowser with its private localhost fixture.
import { readFile, mkdir } from "node:fs/promises"
import { chromium, expect } from "@playwright/test"
import assert from "node:assert/strict"
const fixture = JSON.parse(await readFile(process.env.CREWSHIP_ARTIFACT_PROJECT_FIXTURE, "utf8"))
assert.match(fixture.server, /^http:\/\/127\.0\.0\.1:\d+$/)
const browser = await chromium.launch({ headless: true })
const contexts = []
let diagnosticPage
const output = process.env.CREWSHIP_ARTIFACT_PROJECT_OUTPUT
if (output) await mkdir(output, { recursive: true })
try {
 async function context(user) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });contexts.push(context)
  await context.addCookies([{ name: "authjs.session-token", value: fixture.tokens[user], url: fixture.server, httpOnly: true, sameSite: "Lax" }])
  await context.addInitScript(({workspace,user}) => localStorage.setItem(`crewship.workspaceId:${user}`, workspace), {workspace:fixture.workspace,user})
  return context
 }
 const owner = await context(fixture.owner), reader = await context(fixture.reader)
 const endpoint = `${fixture.server}/api/v1/pages/browser-artifact/artifact`
 const suffix = `?workspace_id=${encodeURIComponent(fixture.workspace)}`
 const url = `${fixture.server}/pages/browser-artifact?artifact=1&workspace_id=${encodeURIComponent(fixture.workspace)}`
 const files = heading => [
  { path: "index.html", media_type: "text/html", content: `<style>body{background:#f8fafc;color:#172554}h1{font-size:40px}.card{padding:32px;border:1px solid #cbd5e1;border-radius:16px}a{display:inline-block;padding:12px;background:#dbeafe;border-radius:8px}</style><main class="card"><p>Crewship project</p><h1 id="hero-heading">${heading}</h1><p>One project, three connected screens.</p><a href="details.html">View details</a></main>` },
  { path: "details.html", media_type: "text/html", content: '<h1 id="details-heading">Details</h1><p>Everything stays in the same version.</p><a href="settings.html">Open settings</a>' },
  { path: "settings.html", media_type: "text/html", content: '<h1 id="settings-heading">Settings</h1><a href="index.html">Back to overview</a>' },
 ]
 const created = await owner.request.post(`${fixture.server}/api/v1/pages/artifacts${suffix}`, {data:{slug:"browser-artifact",name:"Browser artifact",entrypoint:"index.html",files:files("Version one"),message:"Initial three-screen project"}})
 assert.equal(created.status(),201,await created.text())
 const page = await owner.newPage()
 diagnosticPage=page
 await page.goto(url)
 const frame = page.frameLocator('iframe[title="Artifact preview"]')
 await expect(frame.getByRole("heading",{name:"Version one"})).toBeVisible()
 await frame.getByText("View details").click()
 await expect(frame.getByRole("heading",{name:"Details"})).toBeVisible()
 await frame.getByText("Open settings").click()
 await expect(frame.getByRole("heading",{name:"Settings"})).toBeVisible()
 await frame.getByText("Back to overview").click()
 await expect(frame.getByRole("heading",{name:"Version one"})).toBeVisible()
 await page.getByRole("button",{name:"Select an element",exact:true}).click()
 await frame.getByRole("heading",{name:"Version one"}).click()
 await expect(page.getByRole("blockquote")).toContainText("Version one")
 await page.getByLabel("Comment for agent").fill("Change this heading to Version two")
 const commentResponse = page.waitForResponse(response => response.url().includes("/artifact/comments") && response.request().method()==="POST")
 await page.getByRole("button",{name:"Send to agent",exact:true}).click()
 const postedComment = await commentResponse
 assert.equal(postedComment.status(),201,await postedComment.text())
 const comment = await postedComment.json()
 assert.ok(comment.task_id, "comment must enter the agent queue")
 await expect(page.getByText("Change this heading to Version two",{exact:true})).toBeVisible()
 if(output)await page.screenshot({path:`${output}/artifact-v1-comment.png`,fullPage:true})
 let response = await owner.request.post(`${endpoint}/versions${suffix}`,{data:{base_version:1,entrypoint:"index.html",files:files("Version two"),message:"Address selected heading comment"}})
 assert.equal(response.status(),200,await response.text())
 response=await owner.request.post(`${endpoint}/comments/${comment.id}/reply${suffix}`,{data:{version:2,text:"Updated the selected heading in version 2."}})
 assert.equal(response.status(),200,await response.text())
 await page.reload()
 await expect(frame.getByRole("heading",{name:"Version two"})).toBeVisible()
 await expect(page.getByText("Version 2: Updated the selected heading in version 2.")).toBeVisible()
 if(output)await page.screenshot({path:`${output}/artifact-v2.png`,fullPage:true})
 // A stale publisher receives a conflict, leaving the winner intact.
 response=await owner.request.post(`${endpoint}/versions${suffix}`,{data:{base_version:1,entrypoint:"index.html",files:files("Stale competing edit"),message:"stale"}})
 assert.equal(response.status(),409,await response.text())
 let snapshot=await (await owner.request.get(endpoint+suffix)).json()
 assert.equal(snapshot.latest_version,2)
 assert.match(snapshot.manifest.files.find(file=>file.path==="index.html").content,/Version two/)
 // A stale selected element cannot silently become a different target.
 response=await owner.request.post(`${endpoint}/comments${suffix}`,{data:{version:1,path:"index.html",selector:"#hero-heading",quote:"Version one",text:"Stale target",send_to_agent:false}})
 assert.equal(response.status(),409,await response.text())
 await page.getByLabel("Version",{exact:true}).selectOption("1")
 await expect(frame.getByRole("heading",{name:"Version one"})).toBeVisible()
 const restoreResponse=page.waitForResponse(response=>response.url().includes("/artifact/restore")&&response.request().method()==="POST")
 await page.getByRole("button",{name:"Restore version 1",exact:true}).click()
 assert.equal((await restoreResponse).status(),200)
 await expect(page.getByLabel("Version",{exact:true})).toHaveValue("3")
 await expect(frame.getByRole("heading",{name:"Version one"})).toBeVisible()
 // Sharing a previous version pins it independently from latest version 3.
 await page.getByLabel("Version",{exact:true}).selectOption("2")
 const shareResponse=page.waitForResponse(response=>response.url().includes("/artifact/share")&&response.request().method()==="POST")
 await page.getByRole("button",{name:"Share version 2",exact:true}).click()
 assert.equal((await shareResponse).status(),200)
 response=await owner.request.post(`${fixture.server}/__fixture/reader`,{headers:{"X-Fixture-Control":"synthetic-project-control"}})
 assert.equal(response.status(),204)
 const readerPage=await reader.newPage();await readerPage.goto(url)
 await expect(readerPage.getByText("Read only",{exact:true})).toBeVisible()
 await expect(readerPage.frameLocator("iframe").getByRole("heading",{name:"Version two"})).toBeVisible()
 await expect(readerPage.getByRole("button",{name:/Restore version|Share version|Send to agent/})).toHaveCount(0)
 for(const operation of ["versions","restore","share","comments"]){
  response=await reader.request.post(`${endpoint}/${operation}${suffix}`,{data:{base_version:3,version:1}})
  assert.ok([403,404].includes(response.status()),`reader ${operation}: ${response.status()}`)
 }
 assert.equal((await reader.request.get(`${endpoint}${suffix}&version=3`)).status(),404)
 if(output)await readerPage.screenshot({path:`${output}/artifact-shared-reader.png`,fullPage:true})
 // The same persistent SQLite data is opened by a new production Router.
 response=await owner.request.post(`${fixture.server}/__fixture/restart`,{headers:{"X-Fixture-Control":"synthetic-project-control"}})
 assert.equal(response.status(),204)
 await page.goto(url)
 await expect(page.getByLabel("Version",{exact:true})).toHaveValue("3")
 await expect(frame.getByRole("heading",{name:"Version one"})).toBeVisible()
 await readerPage.reload()
 await expect(readerPage.frameLocator("iframe").getByRole("heading",{name:"Version two"})).toBeVisible()
 if(output)await page.screenshot({path:`${output}/artifact-restored-v3-restart.png`,fullPage:true})
 console.log("PASS: real Router/SQLite/browser three-screen project, exact comment, v2, CAS409, stale-anchor409, restore v3, pinned read-only shared v2, reload+Router restart")
} catch(error) { if(output && diagnosticPage) await diagnosticPage.screenshot({path:`${output}/artifact-browser-failure.png`,fullPage:true}).catch(()=>{}); throw error } finally {for(const context of contexts)await context.close();await browser.close()}
