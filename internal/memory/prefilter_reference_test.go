package memory

import "encoding/base64"

func legacyPrefilterScanContent(body string) *ScanHit {
	if body == "" {
		return nil
	}
	// Invisible-unicode check is cheap and high-signal — do it first.
	if hit := scanInvisibleUnicode(body); hit != nil {
		return hit
	}
	for _, r := range scannerRules {
		if r.re.MatchString(body) {
			return &ScanHit{Category: r.category, Pattern: r.name}
		}
	}
	// PR-F4: homoglyph-folded second pass. Catches attacks that
	// substitute a single Cyrillic/Greek look-alike inside a known
	// injection phrase (`іgnore previous instructions` etc.).
	if folded := foldHomoglyphs(body); folded != body {
		for _, r := range scannerRules {
			if r.re.MatchString(folded) {
				return &ScanHit{
					Category: r.category,
					Pattern:  r.name + "_homoglyph",
				}
			}
		}
	}
	// PR-F4: base64-obfuscated payload detection. The encoded form
	// is short and innocuous; the decoded form is the real payload.
	// We only decode blocks >= 60 chars to bound cost and avoid
	// false-positives on incidental base64-shaped substrings (UUIDs,
	// hashes, css minified chunks etc.).
	if hit := legacyPrefilterBase64(body); hit != nil {
		return hit
	}
	return nil
}
func legacyPrefilterBase64(body string) *ScanHit {
	matches := base64Block.FindAllString(body, -1)
	if len(matches) > base64ScanMaxBlocks {
		matches = matches[:base64ScanMaxBlocks]
	}
	for _, match := range matches {
		// Strict StdEncoding; ignore decode errors (most random
		// long base64-shaped strings won't decode cleanly).
		decoded, err := base64.StdEncoding.DecodeString(match)
		if err != nil {
			continue
		}
		// Decoded payload must look like text (>50% printable ASCII)
		// — binary blobs are out of scope and likelier to cause
		// false positives via random regex coincidences.
		if !looksLikeText(decoded) {
			continue
		}
		decodedStr := string(decoded)
		for _, r := range scannerRules {
			if r.re.MatchString(decodedStr) {
				return &ScanHit{
					Category: "base64_obfuscation",
					Pattern:  r.name + "_base64",
				}
			}
		}
		// Also check for invisible-unicode in the decoded payload —
		// double obfuscation is a thing.
		if hit := scanInvisibleUnicode(decodedStr); hit != nil {
			return &ScanHit{
				Category: "base64_obfuscation",
				Pattern:  hit.Pattern + "_base64",
			}
		}
	}
	return nil
}
