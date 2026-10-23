//go:build mooring_rss_fixture

// Explicit local-only measurement harness. Synthetic read-only admission, never a route.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

var versions = mooring.RuntimeVersions{Protocol: "0.4.0", Model: "1.0.0", Engine: "0.1.0"}

func fail(err error) {
	if err != nil {
		panic(err)
	}
}
func frameWrite(w io.Writer, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if len(b) == 0 || len(b) > mooring.MaxRPCFrame {
		return errors.New("fixture frame limit")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	for _, data := range [][]byte{h[:], b} {
		for len(data) > 0 {
			n, e := w.Write(data)
			if e != nil {
				return e
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			data = data[n:]
		}
	}
	return nil
}
func frameRead(r io.Reader) (map[string]any, error) {
	var h [4]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return nil, e
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > mooring.MaxRPCFrame {
		return nil, errors.New("fixture frame limit")
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(r, b); e != nil {
		return nil, e
	}
	var v map[string]any
	e := json.Unmarshal(b, &v)
	return v, e
}
func rss(pid int) uint64 {
	data, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	fail(e)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			v, e := strconv.ParseUint(strings.Fields(line)[1], 10, 64)
			fail(e)
			return v * 1024
		}
	}
	panic("RSS unavailable")
}
func childPID(binaryPath string) int {
	tasks, e := filepath.Glob("/proc/self/task/*/children")
	fail(e)
	for _, p := range tasks {
		data, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		for _, s := range strings.Fields(string(data)) {
			pid, e := strconv.Atoi(s)
			if e != nil {
				continue
			}
			target, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
			if e == nil && target == binaryPath {
				return pid
			}
		}
	}
	panic("own Rust child not found")
}

type host struct {
	runtime *mooring.Runtime
	mu      sync.Mutex
	scope   map[string]any
	wait    sync.WaitGroup
}

func (h *host) call(message map[string]any, reply bool) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, e := json.Marshal(message)
	if e != nil {
		return nil, e
	}
	if e = h.runtime.RPC.Write(raw); e != nil {
		return nil, e
	}
	if !reply {
		return nil, nil
	}
	raw, e = h.runtime.RPC.Read()
	if e != nil {
		return nil, e
	}
	var v map[string]any
	e = json.Unmarshal(raw, &v)
	return v, e
}
func (h *host) serve(c net.Conn) {
	defer h.wait.Done()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(90 * time.Second))
	ticket := "MOCK-READ-ONLY:" + h.scope["application_id"].(string) + "\x00" + h.scope["workspace_id"].(string) + "\x00" + h.scope["artifact_id"].(string)
	joined, e := h.call(map[string]any{"type": "session_join", "request_id": "mock-join", "ticket": ticket, "versions": versions}, true)
	if e != nil {
		return
	}
	session, ok := joined["session_id"].(string)
	if !ok {
		return
	}
	defer func() {
		_, _ = h.call(map[string]any{"type": "session_leave", "request_id": "mock-disconnect", "scope": h.scope, "session_id": session}, false)
	}()
	if frameWrite(c, joined) != nil {
		return
	}
	for {
		request, e := frameRead(c)
		if e != nil {
			return
		}
		if request["type"] != "read_request" {
			return
		}
		// Identity/scope come from this mock connection, never from the client frame.
		response, e := h.call(map[string]any{"type": "read_request", "request_id": "mock-read", "scope": h.scope, "session_id": session, "max_bytes": strconv.Itoa(mooring.MaxRPCFrame)}, true)
		if e != nil || frameWrite(c, response) != nil {
			return
		}
	}
}
func client(address string, count int) {
	connections := make([]net.Conn, 0, count)
	control := bufio.NewReader(os.Stdin)
	for i := 0; i < count; i++ {
		c, e := net.DialTimeout("tcp", address, 3*time.Second)
		fail(e)
		fail(c.SetDeadline(time.Now().Add(60 * time.Second)))
		joined, e := frameRead(c)
		fail(e)
		if joined["type"] != "session_joined" {
			panic("join denied")
		}
		connections = append(connections, c)
	}
	fmt.Println("connected")
	_, e := control.ReadString('\n')
	fail(e)
	for cycle := 0; cycle < 3; cycle++ {
		for _, c := range connections {
			fail(frameWrite(c, map[string]any{"type": "read_request"}))
			v, e := frameRead(c)
			fail(e)
			if v["type"] != "read_result" {
				panic("read denied")
			}
		}
	}
	fmt.Println("traffic")
	_, e = control.ReadString('\n')
	fail(e)
	for _, c := range connections {
		fail(c.Close())
	}
	fmt.Println("disconnected")
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "client" {
		n, e := strconv.Atoi(os.Args[3])
		fail(e)
		client(os.Args[2], n)
		return
	}
	binaryPath := flag.String("binary", "", "explicit test-only rss_fixture executable")
	source := flag.String("source", "", "generic paragraphs JSON input")
	documentCount := flag.Int("documents", 1, "1..100 generic document copies")
	flag.Parse()
	if *documentCount < 1 || *documentCount > 100 {
		panic("document limit")
	}
	absolute, e := filepath.Abs(*binaryPath)
	fail(e)
	absolute, e = filepath.EvalSymlinks(absolute)
	fail(e)
	data, e := os.ReadFile(*source)
	fail(e)
	if len(data) > 200000 {
		panic("fixture input limit")
	}
	var projection map[string]any
	fail(json.Unmarshal(data, &projection))
	if _, ok := projection["paragraphs"].([]any); !ok {
		panic("generic paragraphs required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	runtime, e := mooring.StartRuntime(ctx, mooring.RuntimeConfig{Enabled: true, Binary: absolute, Versions: versions})
	fail(e)
	defer runtime.Close()
	pid := childPID(absolute)
	base := map[string]uint64{"host": rss(os.Getpid()), "rust": rss(pid)}
	h := &host{runtime: runtime, scope: map[string]any{"application_id": "fixture.paragraphs", "workspace_id": "mock-workspace", "artifact_id": "doc-0"}}
	for i := 0; i < *documentCount; i++ {
		scope := map[string]any{"application_id": "fixture.paragraphs", "workspace_id": "mock-workspace", "artifact_id": fmt.Sprintf("doc-%d", i)}
		result, e := h.call(map[string]any{"type": "lifecycle_request", "request_id": "mock-open", "scope": scope, "session_id": "mock-owner", "fence": map[string]any{"generation": "1", "token": "explicit-mock-fence"}, "action": "open", "checkpoint": map[string]any{"scope": scope, "revision": "0", "generation": "1", "format_version": "fixture.paragraphs.v1", "versions": versions, "encoding": "json", "state": string(data), "digest": "sha256:" + strings.Repeat("0", 64), "assets": []any{}}}, true)
		fail(e)
		if result["type"] != "lifecycle_result" {
			panic("load denied")
		}
	}
	time.Sleep(200 * time.Millisecond)
	loaded := map[string]uint64{"host": rss(os.Getpid()), "rust": rss(pid)}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	fail(e)
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			h.wait.Add(1)
			go h.serve(c)
		}
	}()
	records := []map[string]any{}
	for _, count := range []int{1, 10, 100} {
		record := map[string]any{"participants": count, "before": map[string]uint64{"host": rss(os.Getpid()), "rust": rss(pid)}}
		executable, e := os.Executable()
		fail(e)
		cmd := exec.CommandContext(ctx, executable, "client", listener.Addr().String(), strconv.Itoa(count))
		cmd.Env = []string{"LANG=C.UTF-8", "TZ=UTC"}
		control, e := cmd.StdinPipe()
		fail(e)
		out, e := cmd.StdoutPipe()
		fail(e)
		cmd.Stderr = os.Stderr
		fail(cmd.Start())
		scan := bufio.NewScanner(out)
		for _, stage := range []string{"connected", "traffic", "disconnected"} {
			if !scan.Scan() || scan.Text() != stage {
				panic("load-generator stage failed")
			}
			if stage == "disconnected" {
				h.wait.Wait()
			}
			time.Sleep(200 * time.Millisecond)
			record[stage] = map[string]uint64{"host": rss(os.Getpid()), "rust": rss(pid)}
			if stage != "disconnected" {
				_, e = io.WriteString(control, "\n")
				fail(e)
			}
		}
		fail(control.Close())
		fail(cmd.Wait())
		records = append(records, record)
	}
	digest := sha256.Sum256(data)
	result := map[string]any{"mock_authority": true, "phase_settle_ms": 200, "read_only": true, "transport": "loopback TCP participants -> Go host -> private inherited Unix RPC -> Rust fixture", "client_rss_excluded": true, "saved_issuer": false, "documents": *documentCount, "fixture_sha256": hex.EncodeToString(digest[:]), "baseline": base, "loaded": loaded, "records": records}
	fail(json.NewEncoder(os.Stdout).Encode(result))
}
