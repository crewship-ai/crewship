import { describe, expect, it } from 'vitest'
import { staticRaster, staticCSS, STATIC_RASTER_LIMITS } from '../artifact-raster'

// Own 2x3 RGBA PNG, generated with standard PNG chunks/zlib and valid CRCs.
const png = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAEUlEQVR4nGNQSFjwH4QZMBgAo1MMu8a2NUsAAAAASUVORK5CYII='

describe('static raster preflight', () => {
  it('accepts an actual bounded PNG container and checks dimensions', () => {
    expect(staticRaster(png)).toMatchObject({ src: png, width: 2, height: 3, bytes: 74 })
  })
  it.each([
    'https://sink.test/image.png', 'data:image/svg+xml;base64,PHN2Zz4=',
    png.replace('image/png', 'image/jpeg'), png.replace('base64,', 'base64,\n'),
    png.slice(0, -4), png + 'AAAA', 'data:image/png;base64,' + btoa('<svg></svg>'),
    'data:image/png;base64,' + 'A'.repeat(4 * Math.ceil(STATIC_RASTER_LIMITS.imageBytes / 3) + 4),
  ])('refuses unsupported URLs, MIME tricks, noncanonical or unbounded input', value => {
    expect(staticRaster(value)).toBeNull()
  })
  it('refuses CRC corruption and appended data rather than trusting IHDR alone', () => {
    const data = Uint8Array.from(atob(png.split(',')[1]), char => char.charCodeAt(0))
    data[44] ^= 1
    expect(staticRaster('data:image/png;base64,' + btoa(String.fromCharCode(...data)))).toBeNull()
  })
  it.each(['@import "https://sink.test";', 'background:url(https://sink.test)', 'background:u\\72l(https://sink.test)', 'background:image-set("https://sink.test" 1x)', 'background:URL/**/(https://sink.test)', '@font-face{src:local(x)}', '@media screen{@import \"https://sink.test\";}'])('refuses network-bearing CSS: %s', css => {
    expect(staticCSS(css)).toBe(false)
  })
  it('keeps ordinary static styles', () => {
    expect(staticCSS('body{background:#fff;color:rgb(12,34,56);width:calc(100% - 2px)}')).toBe(true)
    expect(staticCSS('@media (min-width:1px){@supports(display:grid){h1{display:grid}}}@keyframes fade{to{opacity:1}}')).toBe(true)
  })
})
