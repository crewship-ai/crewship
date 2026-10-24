package mooring

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
)

// MaxOriginInventoryEntries is the current bounded inspector's limit, not a
// document-lifetime entitlement or a public protocol capability.
const MaxOriginInventoryEntries = 4096

// MaxIssuedOrigin bounds exact client IDs for the current application bridge.
const MaxIssuedOrigin = uint64(1<<53 - 1)

// ErrOriginInventoryData reports invalid or exhausted bounded data, not a
// canonical permission decision or a durable operation outcome.
var ErrOriginInventoryData = errors.New("invalid bounded origin inventory data")

// OriginClockData is untrusted scalar data. Neither its shape nor successful
// normalization proves a checkpoint inventory or authorizes a writer.
type OriginClockData struct {
	Origin string
	Clock  string
}

// OriginInventoryData is normalized DATA ONLY. In particular, callers can
// normalize an empty list; that is never evidence of an empty checkpoint.
// Issuance must separately require the runtime's opaque inventory proof bound
// to the actual store, original authority, owner and retained full checkpoint.
type OriginInventoryData struct {
	entries []OriginClockData
	digest  string
	valid   bool
}

// Entries returns a copy; caller mutation cannot rewrite normalized data.
func (d OriginInventoryData) Entries() []OriginClockData {
	return append([]OriginClockData(nil), d.entries...)
}

// Digest correlates bounded data, not inventory provenance or authority.
func (d OriginInventoryData) Digest() string { return d.digest }

func originDataCounter(value string, maximum uint64, zero bool) (uint64, error) {
	if value == "" || len(value) > 20 || (len(value) > 1 && value[0] == '0') {
		return 0, ErrOriginInventoryData
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, ErrOriginInventoryData
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n > maximum || (!zero && n == 0) {
		return 0, ErrOriginInventoryData
	}
	return n, nil
}

// NormalizeOriginInventoryData rejects noncanonical, duplicate or numerically
// unsorted tuples. Zero is allowed as an occupied baseline origin. Clock zero
// is not an allocated struct frontier. No original input slices are retained.
func NormalizeOriginInventoryData(input []OriginClockData) (OriginInventoryData, error) {
	if len(input) > MaxOriginInventoryEntries {
		return OriginInventoryData{}, ErrOriginInventoryData
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("mooring.origin-inventory-data.v1\x00"))
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(input)))
	_, _ = hash.Write(count[:])
	var previous uint64
	for i, item := range input {
		origin, err := originDataCounter(item.Origin, MaxIssuedOrigin, true)
		if err != nil || (i > 0 && origin <= previous) {
			return OriginInventoryData{}, ErrOriginInventoryData
		}
		clock, err := originDataCounter(item.Clock, 1<<32-1, false)
		if err != nil {
			return OriginInventoryData{}, ErrOriginInventoryData
		}
		var tuple [12]byte
		binary.BigEndian.PutUint64(tuple[:8], origin)
		binary.BigEndian.PutUint32(tuple[8:], uint32(clock))
		_, _ = hash.Write(tuple[:])
		previous = origin
	}
	return OriginInventoryData{entries: append([]OriginClockData(nil), input...), digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), valid: true}, nil
}

// LeastUnusedOriginData computes a candidate from bounded data only. It is NOT
// a reservation or an issuance API. A real issuer must union every persisted
// burned origin under its FULL writer, commit the reservation before publication,
// and separately verify opaque runtime inventory and original author authority.
// Issued includes unused, rejected, closed and historical tombstone origins;
// nothing is removed merely because the current checkpoint no longer shows it.
func LeastUnusedOriginData(baseline OriginInventoryData, issued []string) (string, error) {
	if !baseline.valid || len(issued) > MaxOriginInventoryEntries {
		return "", ErrOriginInventoryData
	}
	occupied := make(map[uint64]struct{}, len(baseline.entries)+len(issued))
	for _, item := range baseline.entries {
		origin, err := originDataCounter(item.Origin, MaxIssuedOrigin, true)
		if err != nil {
			return "", err
		}
		occupied[origin] = struct{}{}
	}
	var previous uint64
	for i, item := range issued {
		origin, err := originDataCounter(item, MaxIssuedOrigin, false)
		if err != nil || (i > 0 && origin <= previous) {
			return "", ErrOriginInventoryData
		}
		occupied[origin] = struct{}{}
		previous = origin
	}
	if len(occupied) >= MaxOriginInventoryEntries {
		return "", ErrOriginInventoryData
	}
	// At most 4095 occupied values can precede a free positive candidate.
	for candidate := uint64(1); candidate <= MaxOriginInventoryEntries; candidate++ {
		if _, exists := occupied[candidate]; !exists {
			return strconv.FormatUint(candidate, 10), nil
		}
	}
	return "", ErrOriginInventoryData
}
