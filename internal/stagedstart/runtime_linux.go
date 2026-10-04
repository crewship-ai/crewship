//go:build linux

package stagedstart

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func peerCredentials(c *net.UnixConn) (*unix.Ucred, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *unix.Ucred
	var e error
	err = raw.Control(func(fd uintptr) { cred, e = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil {
		return nil, err
	}
	if e != nil {
		return nil, e
	}
	return cred, nil
}

// Admission is keyed by the kernel peer UID before a handler is reserved.
// Agent peers cannot exhaust the trusted control pool or extend boot deadlines.
type peerSlots struct {
	control chan struct{}
	agent   chan struct{}
}

func newPeerSlots() peerSlots { return peerSlots{make(chan struct{}, 32), make(chan struct{}, 8)} }
func (s peerSlots) reserve(cred *unix.Ucred) (func(), bool) {
	if cred == nil {
		return nil, false
	}
	var slots chan struct{}
	switch cred.Uid {
	case 1002:
		slots = s.control
	case 1001:
		slots = s.agent
	default:
		return nil, false
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

// Keeper owns the listening socket and boot nonce; no image file is consulted.
func Keeper() error {
	if os.Getuid() != 1002 || os.Getpid() != 1 {
		return errors.New("staged keeper requires PID1 and UID1002")
	}
	if e := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); e != nil {
		return e
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: Socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer l.Close()
	s := State{Nonce: rand.Text() + rand.Text(), Phase: "staging"}
	var mu sync.Mutex
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(done)
	// PID1 must reap orphaned image processes without executing an image init.
	reap := make(chan os.Signal, 1)
	signal.Notify(reap, syscall.SIGCHLD)
	defer signal.Stop(reap)
	go func() {
		for range reap {
			for {
				pid, _ := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
				if pid <= 0 {
					break
				}
			}
		}
	}()
	timer := time.AfterFunc(30*time.Second, func() {
		mu.Lock()
		ready := s.Phase == "ready"
		mu.Unlock()
		if !ready {
			_ = l.Close()
		}
	})
	defer timer.Stop()
	contacted := false
	go func() { <-done; _ = l.Close() }()
	slots := newPeerSlots()
	for {
		c, e := l.AcceptUnix()
		if e != nil {
			return nil
		}
		cred, e := peerCredentials(c)
		if e != nil {
			c.Close()
			continue
		}
		release, admitted := slots.reserve(cred)
		if !admitted {
			c.Close()
			continue
		}
		go func() {
			defer release()
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			var r Request
			if json.NewDecoder(io.LimitReader(c, 4096)).Decode(&r) != nil {
				return
			}
			mu.Lock()
			if r.Operation == "status" && cred.Uid == 1002 && !contacted {
				contacted = true
				timer.Reset(30 * time.Second)
			}
			out := s.Apply(r, cred.Uid)
			mu.Unlock()
			if json.NewEncoder(c).Encode(out) != nil || out.Error != "" || r.Operation != "consume" {
				return
			}
			// Only the accepted, non-dumpable gate owns completion on this connection.
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			var result struct{ Success bool }
			e = json.NewDecoder(io.LimitReader(c, 1024)).Decode(&result)
			mu.Lock()
			if s.Phase == "bootstrapping" {
				s.Phase = "failed"
				if e == nil && result.Success {
					s.Phase = "ready"
				}
			}
			mu.Unlock()
		}()
	}
}

func exchange(r Request) (*net.UnixConn, Status, error) {
	return exchangeAt(Socket, r)
}

func exchangeAt(socket string, r Request) (*net.UnixConn, Status, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, Status{}, err
	}
	cred, err := peerCredentials(c)
	if err != nil || cred.Uid != 1002 || cred.Pid != 1 {
		c.Close()
		return nil, Status{}, errors.New("staged start: peer is not current PID1 keeper")
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err = json.NewEncoder(c).Encode(r); err != nil {
		c.Close()
		return nil, Status{}, err
	}
	var s Status
	err = json.NewDecoder(io.LimitReader(c, 4096)).Decode(&s)
	if err == nil && (s.Error != "" || s.Challenge != r.Challenge) {
		err = errors.New("staged start: keeper denied operation")
	}
	if err != nil {
		c.Close()
		return nil, s, err
	}
	return c, s, nil
}

// Run handles an early explicit binary mode. No flag/parser/auth init precedes it.
func Run(args []string) (int, error) {
	if len(args) == 0 {
		return 126, errors.New("staged start: missing mode")
	}
	switch args[0] {
	case "--staged-keeper":
		if len(args) != 1 {
			return 126, errors.New("staged keeper: invalid arguments")
		}
		return 0, Keeper()
	case "--staged-control":
		if len(args) != 4 {
			return 126, errors.New("staged control: invalid arguments")
		}
		c, s, e := exchange(Request{Operation: args[1], Nonce: args[2], Challenge: args[3]})
		if e != nil {
			return 126, e
		}
		c.Close()
		return 0, json.NewEncoder(os.Stdout).Encode(s)
	case "--staged-bootstrap", "--staged-exec":
		if len(args) < 2 {
			return 126, errors.New("staged gate: missing boot nonce")
		}
		// The image child must not ptrace the supervisor or inherit its control FD.
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
			return 126, err
		}
		operation := "exec"
		if args[0] == "--staged-bootstrap" {
			operation = "consume"
			if len(args) != 2 {
				return 126, errors.New("staged bootstrap: unexpected command")
			}
		}
		c, _, e := exchange(Request{Operation: operation, Nonce: args[1]})
		if e != nil {
			return 126, e
		}
		defer c.Close()
		if operation == "consume" {
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			cmd := exec.Command(Bootstrap, "--bootstrap-only")
			cmd.Env = workloadEnv()
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			e = cmd.Run()
			_ = json.NewEncoder(c).Encode(struct{ Success bool }{e == nil})
			if e != nil {
				var exit *exec.ExitError
				if errors.As(e, &exit) {
					return exit.ExitCode(), nil
				}
				return 126, e
			}
			return 0, nil
		}
		if len(args) < 3 {
			return 126, errors.New("staged exec: empty command")
		}
		env := workloadEnv()
		binary := args[2]
		if !strings.Contains(binary, "/") {
			var e error
			binary, e = exec.LookPath(binary)
			if e != nil {
				return 126, e
			}
		}
		return 126, syscall.Exec(binary, args[2:], env)
	default:
		return 126, fmt.Errorf("staged start: unknown mode")
	}
}

func workloadEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "LD_PRELOAD" || key == "LD_AUDIT" || key == "BASH_ENV" || key == "ENV" || key == "SHELLOPTS" || key == "BASHOPTS" {
			continue
		}
		out = append(out, entry)
	}
	return out
}
