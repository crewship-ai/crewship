package appengine

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestHostContractDeclarationMatchesProtocolConstants(t *testing.T) {
	if !hostContractValid {
		t.Fatal("embedded host contract does not parse")
	}
	want := WorkerContract{Protocol: GridCraftWorkerProtocol, ProtocolMin: GridCraftWorkerProtocolMin, ProtocolMax: GridCraftWorkerProtocolMax, EngineSHA256: GridCraftBinarySHA256}
	if hostContract != want {
		t.Fatalf("host declaration %+v diverges from constants %+v", hostContract, want)
	}
	if GridCraftWorkerProtocol < GridCraftWorkerProtocolMin || GridCraftWorkerProtocol > GridCraftWorkerProtocolMax {
		t.Fatal("this build does not accept its own protocol")
	}
	// The operator launcher scans the release binary with this exact shape.
	launcher := regexp.MustCompile(`\x00crewship-gridcraft-host-contract/v1;protocol=([1-9][0-9]{0,3});protocol-min=([1-9][0-9]{0,3});protocol-max=([1-9][0-9]{0,3});engine-sha256=([0-9a-f]{64})\x00`)
	if got := launcher.FindAllStringSubmatch(gridCraftHostContract, -1); len(got) != 1 || got[0][1] != strconv.Itoa(GridCraftWorkerProtocol) || got[0][4] != GridCraftBinarySHA256 {
		t.Fatalf("launcher cannot read declaration: %q", got)
	}
	image := WorkerImageContract()
	if image["protocol"] != GridCraftWorkerProtocol || image["protocol_min"] != GridCraftWorkerProtocolMin ||
		image["protocol_max"] != GridCraftWorkerProtocolMax || image["engine_sha256"] != GridCraftBinarySHA256 || len(image) != 4 {
		t.Fatalf("image contract: %v", image)
	}
}

func TestParseHostContractRefusesMalformedDeclarations(t *testing.T) {
	engine := strings.Repeat("a", 64)
	for name, text := range map[string]string{
		"empty":           "",
		"no delimiters":   "crewship-gridcraft-host-contract/v1;protocol=1;protocol-min=1;protocol-max=1;engine-sha256=" + engine,
		"own outside":     "\x00crewship-gridcraft-host-contract/v1;protocol=3;protocol-min=1;protocol-max=2;engine-sha256=" + engine + "\x00",
		"no own protocol": "\x00crewship-gridcraft-host-contract/v1;protocol-min=1;protocol-max=1;engine-sha256=" + engine + "\x00",
		"inverted range":  "\x00crewship-gridcraft-host-contract/v1;protocol=1;protocol-min=2;protocol-max=1;engine-sha256=" + engine + "\x00",
		"zero":            "\x00crewship-gridcraft-host-contract/v1;protocol=1;protocol-min=0;protocol-max=1;engine-sha256=" + engine + "\x00",
		"leading zero":    "\x00crewship-gridcraft-host-contract/v1;protocol=1;protocol-min=01;protocol-max=1;engine-sha256=" + engine + "\x00",
		"short engine":    "\x00crewship-gridcraft-host-contract/v1;protocol=1;protocol-min=1;protocol-max=1;engine-sha256=abc\x00",
		"upper engine":    "\x00crewship-gridcraft-host-contract/v1;protocol=1;protocol-min=1;protocol-max=1;engine-sha256=" + strings.ToUpper(engine) + "\x00",
		"other version":   "\x00crewship-gridcraft-host-contract/v2;protocol=1;protocol-min=1;protocol-max=1;engine-sha256=" + engine + "\x00",
		"trailing fields": "\x00crewship-gridcraft-host-contract/v1;protocol=1;protocol-min=1;protocol-max=1;engine-sha256=" + engine + ";x=1\x00",
	} {
		if _, ok := parseHostContract(text); ok {
			t.Errorf("%s: malformed declaration accepted", name)
		}
	}
	got, ok := parseHostContract("\x00crewship-gridcraft-host-contract/v1;protocol=3;protocol-min=2;protocol-max=5;engine-sha256=" + engine + "\x00")
	if !ok || got != (WorkerContract{3, 2, 5, engine}) {
		t.Fatalf("valid declaration: %+v %v", got, ok)
	}
}

func TestWorkerContractCompatibility(t *testing.T) {
	engine := strings.Repeat("e", 64)
	other := strings.Repeat("f", 64)
	c := WorkerContract{Protocol: 3, ProtocolMin: 2, ProtocolMax: 4, EngineSHA256: engine}
	for _, tc := range []struct {
		name, protocol, engine string
		contract               WorkerContract
		want                   bool
	}{
		{"lower bound", "2", engine, c, true},
		{"inside", "3", engine, c, true},
		{"upper bound", "4", engine, c, true},
		{"below", "1", engine, c, false},
		{"above", "5", engine, c, false},
		{"missing protocol", "", engine, c, false},
		{"missing engine", "3", "", c, false},
		{"other engine", "3", other, c, false},
		{"leading zero", "03", engine, c, false},
		{"signed", "+3", engine, c, false},
		{"spaces", " 3", engine, c, false},
		{"huge", "99999999999999999999", engine, c, false},
		{"zero", "0", engine, WorkerContract{1, 0, 4, engine}, false},
		{"invalid local range", "3", engine, WorkerContract{3, 4, 2, engine}, false},
		{"invalid local engine", "3", "", WorkerContract{3, 2, 4, ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.contract.Compatible(tc.protocol, tc.engine); got != tc.want {
				t.Fatalf("Compatible(%q, %q) = %v, want %v", tc.protocol, tc.engine, got, tc.want)
			}
		})
	}
}

func TestPeerCompatibleRequiresExactlyOneValue(t *testing.T) {
	ok := http.Header{}
	setContractHeaders(ok)
	if !peerCompatible(workerContract, ok) || !hostAccepts(ok) {
		t.Fatal("own contract refused")
	}
	for name, mutate := range map[string]func(http.Header){
		"no protocol":    func(h http.Header) { h.Del(workerProtocolHeader) },
		"no engine":      func(h http.Header) { h.Del(workerEngineHeader) },
		"two protocols":  func(h http.Header) { h.Add(workerProtocolHeader, strconv.Itoa(GridCraftWorkerProtocol)) },
		"two engines":    func(h http.Header) { h.Add(workerEngineHeader, GridCraftBinarySHA256) },
		"newer protocol": func(h http.Header) { h.Set(workerProtocolHeader, strconv.Itoa(GridCraftWorkerProtocolMax+1)) },
		"older protocol": func(h http.Header) { h.Set(workerProtocolHeader, strconv.Itoa(GridCraftWorkerProtocolMin-1)) },
		"different pin":  func(h http.Header) { h.Set(workerEngineHeader, strings.Repeat("0", 64)) },
	} {
		h := ok.Clone()
		mutate(h)
		if peerCompatible(workerContract, h) || hostAccepts(h) {
			t.Errorf("%s: incompatible peer accepted", name)
		}
	}
}

// The launcher counts every occurrence of the record name in the release
// binary and requires exactly one, so no other production string may carry it.
func TestHostContractRecordNameOccursOnceInProductionSources(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		count += strings.Count(string(raw), "crewship-gridcraft-host-contract/")
	}
	if count != 1 {
		t.Fatalf("record name occurs %d times in production sources, want 1", count)
	}
}
