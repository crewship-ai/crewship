//go:build linux

// restricted-runtime-probe is a trusted bootstrap for the offline acceptance
// image. It is not a new production entrypoint or an agent-facing API.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "restricted bootstrap failed")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("mode required")
	}
	switch os.Args[1] {
	case "hold":
		for {
			time.Sleep(time.Hour)
		}
	case "launch":
		if os.Getuid() != 1001 {
			return fmt.Errorf("identity")
		}
		var b restrictedruntime.Bootstrap
		dec := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20))
		dec.DisallowUnknownFields()
		if e := dec.Decode(&b); e != nil {
			return e
		}
		if len(b.Command) == 0 {
			return fmt.Errorf("command")
		}
		safe := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
		for k, v := range b.Files {
			if !safe.MatchString(k) {
				return fmt.Errorf("file")
			}
			f, e := os.OpenFile(filepath.Join("/secrets", k), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
			if e != nil {
				return e
			}
			_, e = f.WriteString(v)
			ce := f.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		}
		env := []string{"HOME=/home/agent", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
		keys := make([]string, 0, len(b.Env))
		for k := range b.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			env = append(env, k+"="+b.Env[k])
		}
		exe, e := exec.LookPath(b.Command[0])
		if e != nil {
			return e
		}
		fmt.Println("RESTRICTED_READY")
		return syscall.Exec(exe, b.Command, env)
	case "mock":
		// The only upstream in the acceptance image. The expected value arrives
		// over stdin under UID 1002 and is never returned in a response or log.
		if os.Getuid() != 1002 {
			return fmt.Errorf("identity")
		}
		var cfg struct{ Token, Account string }
		if e := json.NewDecoder(os.Stdin).Decode(&cfg); e != nil {
			return e
		}
		var calls atomic.Int64
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/count" {
				fmt.Fprint(w, calls.Load())
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+cfg.Token {
				w.WriteHeader(401)
				return
			}
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "account": cfg.Account})
		})
		listener, e := net.Listen("tcp", "127.0.0.1:9120")
		if e != nil {
			return e
		}
		fmt.Println("MOCK_READY")
		return (&http.Server{Handler: h, ReadHeaderTimeout: time.Second}).Serve(listener)
	default:
		return fmt.Errorf("mode")
	}
}
