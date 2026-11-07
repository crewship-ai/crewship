// What a screenshot holds, from its pixels: nothing here looks at the page, so a
// style or a DOM node cannot pass for content. 8-bit RGB or RGBA PNG, which is what
// Chromium writes. Background is the most common colour of an image.
import { inflateSync } from "node:zlib"

const paeth = (a, b, c) => {
  const p = a + b - c, pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c)
  return pa <= pb && pa <= pc ? a : pb <= pc ? b : c
}

export function decodePng(file) {
  let at = 8, width = 0, height = 0, type = 0
  const parts = []
  while (at < file.length) {
    const size = file.readUInt32BE(at), kind = file.toString("latin1", at + 4, at + 8), body = file.subarray(at + 8, at + 8 + size)
    if (kind === "IHDR") {
      width = body.readUInt32BE(0); height = body.readUInt32BE(4); type = body[9]
      if (body[8] !== 8 || (type !== 2 && type !== 6) || body[12] !== 0) throw new Error("unsupported PNG")
    }
    if (kind === "IDAT") parts.push(body)
    at += size + 12
  }
  const channels = type === 6 ? 4 : 3, stride = width * channels, raw = inflateSync(Buffer.concat(parts))
  const data = Buffer.alloc(width * height * 4), line = Buffer.alloc(stride), above = Buffer.alloc(stride)
  for (let y = 0; y < height; y++) {
    const start = y * (stride + 1), filter = raw[start]
    for (let x = 0; x < stride; x++) {
      const value = raw[start + 1 + x], left = x >= channels ? line[x - channels] : 0, up = above[x], corner = x >= channels ? above[x - channels] : 0
      line[x] = value + (filter === 1 ? left : filter === 2 ? up : filter === 3 ? (left + up) >> 1 : filter === 4 ? paeth(left, up, corner) : 0)
    }
    for (let x = 0; x < width; x++) {
      data[(y * width + x) * 4] = line[x * channels]; data[(y * width + x) * 4 + 1] = line[x * channels + 1]; data[(y * width + x) * 4 + 2] = line[x * channels + 2]
      data[(y * width + x) * 4 + 3] = channels === 4 ? line[x * 4 + 3] : 255
    }
    line.copy(above)
  }
  return { width, height, data }
}

function background({ data }) {
  const counts = new Map()
  for (let i = 0; i < data.length; i += 4) { const key = data.readUInt32BE(i); counts.set(key, (counts.get(key) ?? 0) + 1) }
  return [...counts].reduce((best, entry) => entry[1] > best[1] ? entry : best)[0]
}
const near = (data, i, colour) => Math.abs(data[i] - (colour >>> 24)) + Math.abs(data[i + 1] - ((colour >>> 16) & 255)) + Math.abs(data[i + 2] - ((colour >>> 8) & 255)) < 24

/** How much of the image is not background. Zero is blank or one colour. */
export function measureImage(image) {
  const colour = background(image), pixels = image.data.length / 4
  let content = 0
  for (let i = 0; i < image.data.length; i += 4) if (!near(image.data, i, colour)) content++
  return { contentShare: content / pixels, pixels }
}

/** Of what was content before, the share that is background in `after`. */
export function compareImages(before, after) {
  if (before.width !== after.width || before.height !== after.height) return { lost: 1, resized: true }
  const was = background(before), now = background(after)
  let content = 0, gone = 0
  for (let i = 0; i < before.data.length; i += 4) {
    if (near(before.data, i, was)) continue
    content++
    if (near(after.data, i, now)) gone++
  }
  return { lost: content ? gone / content : 1, resized: false }
}
