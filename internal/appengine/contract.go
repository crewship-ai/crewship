package appengine

import (
	"net/http"
	"regexp"
	"strconv"
)

// GridCraftWorkerProtocol is the version of the private worker<->host
// contract: the service envelope, the admitted commands and the snapshot
// semantics both sides rely on. Bump it for any incompatible change. Each
// side refuses a peer whose version is outside its accepted range, so a host
// and a worker built from different sources may run together only when this
// contract and the pinned engine identity agree.
const GridCraftWorkerProtocol = 1

// The range of worker protocol versions this build accepts from its peer.
// Widen it only when the older peer version has been verified compatible.
const (
	GridCraftWorkerProtocolMin = 1
	GridCraftWorkerProtocolMax = 1
)

const (
	workerProtocolHeader = "X-Crewship-Gridcraft-Protocol"
	workerEngineHeader   = "X-Crewship-Gridcraft-Engine"
)

// gridCraftHostContract is the application's declared worker contract,
// embedded verbatim in the application binary. An operator launcher reads it
// from the released, checksum-verified binary as data (nothing is executed)
// and starts a qualified worker image only when the image's protocol is in
// this range and its engine pin equals this one. It is not served over any
// network endpoint. The host's own handshake is derived from the same text,
// so the declaration and the enforcement cannot diverge.
const gridCraftHostContract = "\x00crewship-gridcraft-host-contract/v1;protocol-min=1;protocol-max=1;engine-sha256=" + GridCraftBinarySHA256 + "\x00"

var hostContractPattern = regexp.MustCompile(`^\x00crewship-gridcraft-host-contract/v1;protocol-min=([1-9][0-9]{0,3});protocol-max=([1-9][0-9]{0,3});engine-sha256=([0-9a-f]{64})\x00$`)

// WorkerContract is one side's declaration of the protocol range it accepts
// and the engine pin it carries.
type WorkerContract struct {
	ProtocolMin, ProtocolMax int
	EngineSHA256             string
}

func parseHostContract(text string) (WorkerContract, bool) {
	m := hostContractPattern.FindStringSubmatch(text)
	if m == nil {
		return WorkerContract{}, false
	}
	lo, _ := strconv.Atoi(m[1])
	hi, _ := strconv.Atoi(m[2])
	if lo > hi {
		return WorkerContract{}, false
	}
	return WorkerContract{ProtocolMin: lo, ProtocolMax: hi, EngineSHA256: m[3]}, true
}

// hostContract is what WorkerClient enforces; a malformed declaration makes
// every worker response incompatible rather than silently accepting one.
var hostContract, hostContractValid = parseHostContract(gridCraftHostContract)

// workerContract is what the worker handler enforces on the host.
var workerContract = WorkerContract{ProtocolMin: GridCraftWorkerProtocolMin, ProtocolMax: GridCraftWorkerProtocolMax, EngineSHA256: GridCraftBinarySHA256}

var protocolValue = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)

// Compatible reports whether a peer speaking protocol with engine pin engine
// may be used. Missing, malformed, out-of-range or different-engine peers are
// refused; there is no default or fallback version.
func (c WorkerContract) Compatible(protocol, engine string) bool {
	if c.ProtocolMin < 1 || c.ProtocolMin > c.ProtocolMax || len(c.EngineSHA256) != 64 {
		return false
	}
	if len(protocol) == 0 || !protocolValue.MatchString(protocol) {
		return false
	}
	n, err := strconv.Atoi(protocol)
	if err != nil || n < c.ProtocolMin || n > c.ProtocolMax {
		return false
	}
	return engine == c.EngineSHA256
}

func setContractHeaders(h http.Header) {
	h.Set(workerProtocolHeader, strconv.Itoa(GridCraftWorkerProtocol))
	h.Set(workerEngineHeader, GridCraftBinarySHA256)
}

// peerCompatible reads exactly one protocol and engine header value.
func peerCompatible(c WorkerContract, h http.Header) bool {
	p, e := h.Values(workerProtocolHeader), h.Values(workerEngineHeader)
	return len(p) == 1 && len(e) == 1 && c.Compatible(p[0], e[0])
}

// WorkerImageContract is what a worker image declares for its qualification
// receipt: the protocol it speaks and the engine pin it carries.
func WorkerImageContract() map[string]any {
	return map[string]any{"protocol": GridCraftWorkerProtocol, "engine_sha256": GridCraftBinarySHA256}
}

func hostAccepts(h http.Header) bool { return hostContractValid && peerCompatible(hostContract, h) }
