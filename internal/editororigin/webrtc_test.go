package editororigin

import (
	"strings"
	"testing"
)

// CSP connect-src does not govern ICE/STUN, the iframe sandbox does not block
// RTCPeerConnection and Chromium does not yet enforce the webrtc directive, so
// a bundle that uses WebRTC has an egress channel outside the editor policy.
// Bundle qualification refuses bundles that reference the WebRTC API.
func TestBundlesReferencingWebRTCAreRefused(t *testing.T) {
	for _, tc := range []struct{ file, body string }{
		{"app.js", "const pc = new RTCPeerConnection({iceServers:[{urls:'stun:198.51.100.1'}]})"},
		{"app.js", "pc.createDataChannel('x'); const c = RTCDataChannel"},
		{"vendor/legacy.mjs", "window.webkitRTCPeerConnection || window.mozRTCPeerConnection"},
		{"app_bg.wasm", "\x00asm\x01\x00\x00\x00\x02\x13\x01\x03wbg\x0bRTCIceTransport"},
		{"index.html", "<!doctype html><script>new RTCSessionDescription({})</script>"},
		{"app.js", "RTCRtpSender.getCapabilities('audio')"},
	} {
		t.Run(tc.file+" "+tc.body[:min(24, len(tc.body))], func(t *testing.T) {
			files := map[string]string{"index.html": "<!doctype html>", tc.file: tc.body}
			for _, mode := range []Options{{Origin: editor}, {SameHost: true}} {
				root := t.TempDir()
				digest := writeBundle(t, root, files)
				mode.StudioOrigin, mode.BundleDir, mode.Bundles = studio, root, []string{digest}
				h, err := New(mode)
				if err == nil || !strings.Contains(err.Error(), "WebRTC") {
					t.Fatalf("same-host=%v: bundle referencing WebRTC accepted (err=%v)", mode.SameHost, err)
				}
				if _, ok := h.bundles[digest]; ok {
					t.Fatalf("same-host=%v: refused bundle is served", mode.SameHost)
				}
			}
		})
	}
	// Ordinary code that merely mentions similar words still qualifies.
	root := t.TempDir()
	digest := writeBundle(t, root, map[string]string{"index.html": "<!doctype html>", "app.js": "const RTC = 1; const peerConnection = {}; fetch('/data')"})
	if _, err := New(Options{Origin: editor, StudioOrigin: studio, BundleDir: root, Bundles: []string{digest}}); err != nil {
		t.Fatalf("bundle without WebRTC refused: %v", err)
	}
}
