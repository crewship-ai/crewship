import assert from 'node:assert/strict'
import { performance } from 'node:perf_hooks'

// The calibration bounds a clock offset; it does not identify an exact key
// timestamp. Overlap is an instrument limitation, not a product failure.
export function classifyClockWindow(interval, dispatchStart, observed) {
  assert.ok([interval.earliest, interval.latest, dispatchStart, observed].every(Number.isFinite))
  if (interval.earliest > interval.latest || interval.earliest > observed || interval.latest < dispatchStart) return 'inconsistent'
  if (interval.latest > observed || interval.earliest < dispatchStart) return 'overlap'
  return 'consistent'
}

// Passive test instrumentation in the actual editor frame. No commands,
// selection changes, synthetic input or render callbacks are produced here.
export async function observeKeys(canvas) {
  const handle = await canvas.elementHandle()
  assert.ok(handle)
  const frame = await handle.ownerFrame()
  assert.ok(frame)
  await frame.evaluate(() => {
    const key = '__wordcraftNightPassiveKeys'
    if (window[key]) return
    const events = []
    window[key] = events
    addEventListener('keydown', event => {
      if (events.length >= 2048) events.shift()
      events.push({ key: event.key, code: event.code, isTrusted: event.isTrusted, timeStamp: event.timeStamp, timeOrigin: performance.timeOrigin })
    }, true)
  })
  const calibration = []
  for (let index = 0; index < 7; index++) {
    const before = performance.timeOrigin + performance.now()
    const browser = await frame.evaluate(() => performance.timeOrigin + performance.now())
    const after = performance.timeOrigin + performance.now()
    calibration.push({ roundtripMs: after - before, offsetMinMs: browser - after, offsetMaxMs: browser - before })
  }
  const best = calibration.reduce((a, b) => a.roundtripMs <= b.roundtripMs ? a : b)
  return {
    calibration: { samples: calibration, selected: best, uncertaintyMs: best.offsetMaxMs - best.offsetMinMs },
    cursor: async () => frame.evaluate(() => window.__wordcraftNightPassiveKeys.length),
    since: async cursor => frame.evaluate(cursor => window.__wordcraftNightPassiveKeys.slice(cursor), cursor),
    nodeInterval(event) {
      assert.equal(event.isTrusted, true)
      assert.equal(event.code, 'KeyW')
      assert.equal(event.key, 'w')
      assert.ok(Number.isFinite(event.timeStamp) && Number.isFinite(event.timeOrigin))
      const epoch = event.timeOrigin + event.timeStamp
      return { earliest: epoch - best.offsetMaxMs - performance.timeOrigin, latest: epoch - best.offsetMinMs - performance.timeOrigin }
    },
  }
}
