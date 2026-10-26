#!/usr/bin/env node
/** Real UI only. Defaults: three full 900s observation windows after successful
 * native startup. Unknown originals are archived, never replayed or reloaded. */
import { chromium } from '@playwright/test'
import { constants } from 'node:fs'
import { appendFile, chmod, mkdir, open, writeFile } from 'node:fs/promises'
import { resolve, join } from 'node:path'
import { performance } from 'node:perf_hooks'
import { setTimeout as sleep } from 'node:timers/promises'
import { advanceProjection, canonicalRefreshChallenge, configuration, failureCode, hasUnsettledInput, hostStatusKind, inputDigest, nativeText, observeFullWindow, progress, reloadVerdict, runVerdict, syntheticOriginal, validateReceipt, validateResponse } from './wordcraft-typing-endurance-helpers.mjs'

async function copyPrivateState(source, destination) {
  const file = await open(source, constants.O_RDONLY | constants.O_NOFOLLOW)
  try {
    const stat = await file.stat()
    if (!stat.isFile() || (stat.mode & 0o777) !== 0o600) throw Error('private_auth_state_required')
    await writeFile(destination, await file.readFile(), { flag: 'wx', mode: 0o600 })
  } finally { await file.close() }
}

async function main() {
  const config = configuration(process.env), output = resolve(config.output)
  // A new private directory prevents mixed runs, stale reports and symlink writes.
  // The caller creates its parent; no existing output directory is adopted.
  await mkdir(output, { mode: 0o700 })
  let auth = join(output, 'auth-run-1.json')
  await copyPrivateState(resolve(config.auth), auth)
  const event = value => appendFile(join(output, 'events.ndjson'), JSON.stringify({ utc: new Date().toISOString(), ...value }) + '\n', { mode: 0o600 })
  const summary = {
    expectedSHA: config.sha, durationSeconds: config.duration, requestedRuns: config.runs, intervalMs: config.interval, result: 'running',
    evidenceScope: 'single-client normal UI, passive HTTP durable text, host lifecycle and conditional reload; screenshots are review evidence, not automatic glyph/paint assertions',
    paintVerified: false,
    timingDefinition: { elapsedMs: 'full monotonic observation horizon, including time after typing stops', typingDurationMs: 'span before first failure, bounded by requested horizon; not continuous physical keystroke time', lastInputOfferedAfterMs: 'last attempted UI key dispatch relative to window start' },
    runs: [],
  }
  const save = () => writeFile(join(output, 'summary.json'), JSON.stringify(summary, null, 2) + '\n', { mode: 0o600 })
  await save()
  let browser
  try {
    browser = await chromium.launch({ headless: true, args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-webgl', '--enable-unsafe-swiftshader'] })
    for (let index = 1; index <= config.runs; index++) {
      const run = { index, state: 'preparing', phase: 'auth', offered: 0, confirmed: 0, durableReceipts: 0, applied: 0, offeredText: '', confirmedText: '', failure: null, snapshots: [], evidenceErrors: [], initialization: { ready: false, phase: 'preflight' }, completedWindow: false, elapsedMs: 0, typingDurationMs: 0, paintVerified: false }
      summary.runs.push(run); await save()
      let context, page, host, status, canvas, frame, actor, pageID, projection, openCount = 0, initialized = false, retiring = false
      let start = null, deadline = null, typingStoppedAt = null, activeAttempt = null, nextSnapshot = 0, snapshotIndex = 0
      const operations = new Map(), deferredResponses = [], tasks = new Set()
      const fail = (reason, details = {}) => {
        if (retiring || run.failure) return
        run.failure = { reason, phase: run.phase, ...details }
        if (start !== null) { typingStoppedAt = performance.now(); run.failure.afterMs = typingStoppedAt - start }
      }
      const note = async value => { try { await event({ run: index, ...value }) } catch { fail('artifact_write_failed') } }
      const track = promise => {
        const safe = promise.catch(() => { fail('invalid_command_observation') })
        tasks.add(safe); void safe.finally(() => tasks.delete(safe))
      }
      const commandPage = request => {
        const url = new URL(request.url())
        if (request.method() !== 'POST' || url.origin !== config.origin || url.searchParams.get('workspace_id') !== config.workspace) return null
        const match = /^\/api\/v1\/pages\/([^/]+)\/mooring\/command-session$/.exec(url.pathname)
        if (!match) return null
        const id = decodeURIComponent(match[1])
        return id.startsWith('@') ? id.slice(1) : null
      }
      const observeRequest = async request => {
        const id = commandPage(request)
        if (!id || id !== pageID || retiring) return
        const input = request.postDataJSON()
        if (input?.action !== 'submit') return
        if (typeof input.raw !== 'string' || Buffer.byteLength(input.raw) > 65536) { fail('invalid_submit_observation'); return }
        const raw = JSON.parse(input.raw), digest = inputDigest(input.raw), prior = operations.get(raw.op_id)
        if (prior) {
          if (prior.digest !== digest || prior.view !== input.view) fail('original_operation_changed')
          else prior.requestObservations++ // Normal pre-handler401 refresh, identical bytes only.
          return
        }
        const synthetic = syntheticOriginal(raw, actor, projection)
        const record = { digest, view: input.view, requestObservations: 1, committed: false, originalFile: null, command: raw.command }
        operations.set(raw.op_id, record)
        if (!activeAttempt || activeAttempt.opId || !synthetic || raw.command !== activeAttempt.command) fail('unexpected_ui_submit')
        if (activeAttempt && !activeAttempt.opId) activeAttempt.opId = raw.op_id
        if (synthetic) {
          // Only this harness's synthetic A/Enter originals are archived. No
          // headers, cookie, arbitrary params, response body or auth URL is logged.
          record.originalFile = `run-${index}-original-${digest.slice('sha256:'.length)}.json`
          await writeFile(join(output, record.originalFile), JSON.stringify({ action: 'submit', view: input.view, raw: input.raw }) + '\n', { flag: 'wx', mode: 0o600 })
        }
        await note({ kind: 'original-observed', operation: typeof raw.op_id === 'string' ? raw.op_id : null, inputDigest: digest, originalFile: record.originalFile })
      }
      const observeResponse = async response => {
        const id = commandPage(response.request())
        if (!id || retiring) return
        if (!pageID) { deferredResponses.push(response); return }
        if (id !== pageID) return
        const input = response.request().postDataJSON(), action = ['open', 'read', 'submit', 'outcome'].includes(input?.action) ? input.action : 'other'
        const value = await response.json().catch(() => null)
        if (canonicalRefreshChallenge(response.status(), value)) { await note({ kind: 'canonical-auth-refresh-challenge', action }); return }
        if (response.status() >= 400) { fail('command_http_failure', { action, httpStatus: response.status() }); await note({ kind: 'command-http-failure', action, httpStatus: response.status() }); return }
        if (action === 'other') throw Error('invalid_action_observation')
        validateResponse(value, actor)
        if (action !== 'open' && input.view !== value.view) throw Error('invalid_view_observation')
        let checked, original
        if (action === 'submit' || action === 'outcome' && value.outcome !== null) {
          checked = validateReceipt(value, { ...input, action: 'submit' }, actor)
          original = operations.get(checked.opId)
          if (!original || original.digest !== checked.digest || original.view !== input.view) throw Error('unobserved_original')
        } else if (value.outcome !== null) throw Error('unexpected_receipt')
        projection = advanceProjection(projection, value.projection)
        // Publish projection and its validated receipt atomically before async
        // artifact I/O; the next key must not compare against a stale ID set.
        if (original && !original.committed) {
          original.committed = true; original.revision = checked.revision; original.projection = value.projection; run.durableReceipts++
          await note({ kind: 'durable-receipt', revision: checked.revision, operation: checked.opId, inputDigest: checked.digest })
        }
        if (action === 'open') {
          openCount++
          if (input.create === true && projection.revision === 0 && nativeText(projection) === '') initialized = true
        }
      }
      const statusKind = async () => hostStatusKind(await status?.textContent({ timeout: 500 }).catch(() => null))
      const evidence = async label => {
        const image = `run-${index}-${++snapshotIndex}-${label}.png`
        try { await page.screenshot({ path: join(output, image), timeout: 5000 }); run.snapshots.push(image); return image }
        catch { run.evidenceErrors.push('screenshot_unavailable'); fail('screenshot_unavailable'); return null }
      }
      const initializationEvidence = async label => {
        const hostStatus = await statusKind()
        const visibleCanvas = await canvas?.isVisible().catch(() => false) ?? false
        const loading = await frame?.locator('#wordcraft_loading').textContent({ timeout: 300 }).catch(() => null)
        const loaderState = typeof loading === 'string' && loading.startsWith('WordCraft could not start') ? 'native_start_failed' : typeof loading === 'string' ? 'loading' : 'not_present_or_unreadable'
        const alertPresent = !!(await host?.getByRole('alert').count().catch(() => 0))
        const screenshot = await evidence(label)
        const item = { phase: run.phase, failureCode: run.failure?.reason ?? null, hostStatus, visibleCanvas, loaderState, hostAlert: alertPresent ? 'host_alert_present' : 'none', screenshot }
        run.initialization = { ...run.initialization, ...item }; await note({ kind: 'initialization-evidence', ...item }); await save()
        return item
      }
      const awaitNativeReady = async (timeoutMs, captureStartup, allowPriorFailure = false) => {
        const until = performance.now() + timeoutMs
        if (captureStartup) await initializationEvidence('startup-before')
        while (performance.now() < until) {
          const kind = await statusKind(), visible = await canvas.isVisible().catch(() => false)
          const loading = await frame.locator('#wordcraft_loading').textContent({ timeout: 200 }).catch(() => null)
          if (typeof loading === 'string' && loading.startsWith('WordCraft could not start')) { fail('native_start_failed'); break }
          if (['stopped', 'unknown', 'reader'].includes(kind)) { fail('native_host_not_ready', { hostStatus: kind }); break }
          if (run.failure && !allowPriorFailure) break
          if (['ready', 'saved'].includes(kind) && visible) return true
          await sleep(200)
        }
        return false
      }
      try {
        context = await browser.newContext({ storageState: auth, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1 })
        page = await context.newPage()
        page.on('request', request => { track(observeRequest(request)) })
        page.on('response', response => { track(observeResponse(response)) })
        page.on('requestfailed', request => {
          try {
            if (!retiring && pageID && commandPage(request) === pageID) {
              const input = request.postDataJSON(), action = ['open', 'read', 'submit', 'outcome'].includes(input?.action) ? input.action : 'other'
              if (action === 'submit') fail('submit_transport_unknown')
              // Cancelling an idle read before a submit is normal, not a failure.
              track(note({ kind: 'command-request-failed', action }))
            }
          } catch { fail('invalid_command_observation') }
        })
        page.on('pageerror', () => { fail('browser_page_error') })
        page.on('crash', () => { fail('browser_page_crash') })
        page.on('close', () => { fail('browser_page_closed') })
        run.phase = 'auth'
        const session = await context.request.get(config.origin + '/api/auth/session', { timeout: 15000 })
        const identity = session.ok() ? (await session.json()).user?.id : null
        if (typeof identity !== 'string' || !identity) throw Error('auth')
        actor = `human:${identity}`
        run.phase = 'version'
        const version = await context.request.get(config.origin + '/api/v1/system/version', { timeout: 15000 })
        if ([401, 403].includes(version.status())) { run.phase = 'auth'; throw Error('auth') }
        if (!version.ok() || (await version.json()).commit !== config.sha) throw Error('version')
        run.phase = 'navigate'
        await page.goto(config.origin + '/pages?workspace_id=' + encodeURIComponent(config.workspace), { waitUntil: 'domcontentloaded', timeout: 45000 })
        run.phase = 'new_document'
        await page.getByRole('button', { name: 'New Doc', exact: true }).click({ timeout: 30000 })
        await page.locator('#document-format').selectOption('commands')
        const slug = 'typing-endurance-' + Date.now() + '-' + index, name = 'Typing endurance ' + index + ' ' + Date.now()
        await page.locator('#document-name').fill(name); await page.locator('#document-slug').fill(slug)
        run.phase = 'create'
        const createdResponse = page.waitForResponse(response => {
          const url = new URL(response.url()), request = response.request()
          if (url.origin !== config.origin || url.pathname !== '/api/v1/pages' || url.searchParams.get('workspace_id') !== config.workspace || request.method() !== 'POST' || response.status() !== 201) return false
          const input = request.postDataJSON(); return input?.slug === slug && input?.name === name
        }, { timeout: 45000 })
        // Attach rejection handling before click, so a failed click cannot leave
        // an unhandled rejected waiter; the original create is never repeated.
        void createdResponse.catch(() => {})
        await page.getByRole('button', { name: 'Create Doc', exact: true }).click()
        const created = await (await createdResponse).json()
        if (typeof created.id !== 'string' || !created.id || created.slug !== slug) throw Error('create')
        pageID = created.id; run.pageID = pageID; run.slug = slug
        deferredResponses.splice(0).forEach(response => { track(observeResponse(response)) })
        run.phase = 'route'
        await page.waitForURL(url => url.origin === config.origin && url.pathname === '/pages/' + slug, { timeout: 45000 })
        run.documentPath = '/pages/' + slug
        host = page.locator('section[aria-label$=" command editor"]'); status = host.getByRole('status')
        frame = page.frameLocator('iframe[title="WordCraft command editor"]'); canvas = frame.locator('canvas').first()
        run.phase = 'initialize'
        if (!await awaitNativeReady(60000, true) || !initialized || !openCount || !projection || projection.revision !== 0 || nativeText(projection) !== '') throw Error('initialize')
        run.initialization.ready = true; run.initialization.phase = 'ready'; run.initialRevision = projection.revision
        await initializationEvidence('startup-ready')
        if (run.failure) throw Error('initialize')
        run.phase = 'focus'
        await canvas.click({ position: { x: 200, y: 280 }, timeout: 10000 })
        if (run.failure) throw Error('focus')
        await page.keyboard.press('Control+End')
        if (run.failure) throw Error('focus')
        start = performance.now(); deadline = start + config.duration * 1000; nextSnapshot = start
        run.startedUTC = new Date().toISOString(); run.state = 'observing'; run.phase = 'typing'; await save()
        await observeFullWindow({ start, durationMs: config.duration * 1000, intervalMs: config.interval, now: () => performance.now(), sleep,
          onStepError: () => { fail(failureCode(run.phase)) },
          step: async ({ now }) => {
            const kind = await statusKind()
            if (!run.failure && !['ready', 'saving', 'saved'].includes(kind)) fail('editor_stopped', { hostStatus: kind })
            if (!run.failure) {
              run.phase = 'typing'
              const enter = run.offered > 0 && run.offered % 50 === 0
              activeAttempt = { command: enter ? 'text.newParagraph' : 'text.insert', addition: enter ? '\n' : 'A', opId: null }
              run.offered++; run.offeredText += activeAttempt.addition; run.lastInputOfferedAfterMs = performance.now() - start
              await page.keyboard.press(enter ? 'Enter' : 'A')
              const settleDeadline = performance.now() + 8000
              while (!run.failure && (!activeAttempt.opId || !operations.get(activeAttempt.opId)?.committed) && performance.now() < settleDeadline) {
                const currentStatus = await statusKind()
                if (['unknown', 'stopped', 'missing'].includes(currentStatus)) fail('editor_stopped', { hostStatus: currentStatus })
                else await sleep(50)
              }
              const operation = activeAttempt.opId && operations.get(activeAttempt.opId)
              if (!run.failure && !operation?.committed) fail('input_without_durable_receipt')
              if (!run.failure) {
                const expected = run.confirmedText + activeAttempt.addition
                if (nativeText(operation.projection) !== expected) fail('confirmed_text_differs_from_offered_input')
                else {
                  run.confirmed++; run.confirmedText = expected
                  try { await status.filter({ hasText: /^Saved$/ }).waitFor({ timeout: 8000 }); run.applied++ }
                  catch { fail('saved_or_apply_not_observed') }
                }
              }
              if (!run.failure) activeAttempt = null
            }
            Object.assign(run, progress(start, deadline, performance.now(), typingStoppedAt)); run.revision = projection?.revision
            if (now >= nextSnapshot || run.failure && !run.failureRecorded) {
              await evidence(run.failure ? 'failed-observing' : 'observing')
              await note({ kind: run.failure ? 'typing-stopped-no-replay' : 'heartbeat', elapsedMs: run.elapsedMs, offered: run.offered, confirmed: run.confirmed, durableReceipts: run.durableReceipts, hostStatus: kind, failureCode: run.failure?.reason ?? null })
              if (run.failure) run.failureRecorded = true
              nextSnapshot = performance.now() + 60000
            }
            await save()
          },
        })
        Object.assign(run, progress(start, deadline, performance.now(), typingStoppedAt))
        run.offeredTextFullyConfirmed = run.confirmed === run.offered
        run.phase = 'reload'
        const unsettled = hasUnsettledInput({ offered: run.offered, confirmed: run.confirmed, applied: run.applied, activeAttempt, operations: [...operations.values()] })
        if (unsettled) {
          run.reload = reloadVerdict({ unsettled: true })
          await note({ kind: 'reload-skipped-unknown-original', retainedOriginals: [...operations.values()].filter(operation => !operation.committed).map(operation => ({ inputDigest: operation.digest, originalFile: operation.originalFile })) })
          await evidence('unknown-original-retained')
        } else {
          const before = nativeText(projection), opens = openCount
          await page.reload({ waitUntil: 'domcontentloaded', timeout: 45000 })
          const until = performance.now() + 45000
          while (openCount === opens && performance.now() < until) await sleep(100)
          const rendererReady = await awaitNativeReady(45000, false, true)
          run.reload = reloadVerdict({ unsettled: false, before, after: nativeText(projection), opened: openCount > opens, rendererReady })
          await evidence('reload')
          if (run.reload.state !== 'confirmed_data_preserved') fail('reload_not_preserved_or_ready')
        }
      } catch {
        fail(failureCode(run.phase))
        if (!run.initialization.ready && page) await initializationEvidence('initialization-failed')
      } finally {
        // All waits are bounded and independent of page lifetime. A failed page
        // never turns into early completion of an already-started window.
        if (start !== null && performance.now() < deadline) {
          await observeFullWindow({ start, durationMs: config.duration * 1000, intervalMs: 1000, now: () => performance.now(), sleep,
            onStepError: () => { fail('observation_unavailable') }, step: async () => { Object.assign(run, progress(start, deadline, performance.now(), typingStoppedAt)); await save() },
          })
        }
        if (start !== null) Object.assign(run, progress(start, deadline, performance.now(), typingStoppedAt))
        if (context) {
          run.phase = 'carry_auth'
          const carried = join(output, `auth-carry-${index}.json`)
          try { await writeFile(carried, '', { flag: 'wx', mode: 0o600 }); await context.storageState({ path: carried }); await chmod(carried, 0o600); auth = join(output, `auth-run-${index + 1}.json`); await copyPrivateState(carried, auth) }
          catch { fail('auth_state_carry_failed') }
        }
        retiring = true
        await context?.close().catch(() => {})
        await Promise.race([Promise.allSettled([...tasks]), sleep(5000)])
        run.state = runVerdict(run); run.phase = 'complete'; await save()
      }
    }
    summary.result = summary.runs.length === config.runs && summary.runs.every(run => run.state === 'passed' && run.completedWindow) ? 'passed' : 'failed'
  } catch { summary.result = 'failed'; summary.failureCode = 'harness_lifecycle_failed' }
  finally { await save(); await browser?.close().catch(() => {}) }
  console.log(JSON.stringify({ result: summary.result, paintVerified: false, runs: summary.runs.map(({ index, state, completedWindow, elapsedMs, typingDurationMs, offered, confirmed, failure }) => ({ index, state, completedWindow, elapsedMs, typingDurationMs, offered, confirmed, failure })) }))
  if (summary.result !== 'passed') process.exitCode = 1
}
void main().catch(() => { console.error(JSON.stringify({ result: 'harness_failed', reason: 'configuration_or_artifact_setup_failed' })); process.exitCode = 1 })
