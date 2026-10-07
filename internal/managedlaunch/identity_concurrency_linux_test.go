//go:build linux

package managedlaunch

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagedRunIdentityAllowsOneConcurrentAdmission(t *testing.T) {
	if id := os.Getenv("CREWSHIP_IDENTITY_CONFORMANCE_CHILD"); id != "" {
		if !ValidRunID(id) {
			os.Exit(93)
		}
		fmt.Println("READY")
		if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
			os.Exit(93)
		}
		if err := establishRunIdentity(id); err != nil {
			if err.Error() != "managed launch: exclusive run identity unavailable" {
				fmt.Print("UNEXPECTED: " + err.Error())
				os.Exit(93)
			}
			fmt.Print("DENIED")
		} else {
			fmt.Print("OWNED")
		}
		os.Exit(0)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	id := "concurrent-" + hex.EncodeToString(nonce)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var children []*exec.Cmd
	defer func() {
		path := DirectRunPIDFile(id)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) {
			t.Error("cannot prove owned identity cleanup")
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			return
		}
		fields := strings.Fields(string(raw))
		if len(fields) != 2 {
			t.Error("invalid owned identity cleanup evidence")
			return
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Error(err)
			return
		}
		owned := false
		for _, child := range children {
			if child.Process != nil && child.Process.Pid == pid {
				owned = true
			}
		}
		if !owned {
			t.Error("foreign identity cleanup refused")
			return
		}
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	}()
	type peer struct {
		command *exec.Cmd
		in      io.WriteCloser
		out     *bufio.Reader
		waiting *bool
	}
	var peers []peer
	for i := 0; i < 2; i++ {
		cmd := exec.CommandContext(ctx, self, "-test.run=^TestManagedRunIdentityAllowsOneConcurrentAdmission$")
		cmd.Env = append(os.Environ(), "CREWSHIP_IDENTITY_CONFORMANCE_CHILD="+id)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
		// The parent alone assigns Wait ownership. Never read ProcessState
		// concurrently with Cmd.Wait or invoke two Wait calls on a child.
		waiting := new(bool)
		defer func() {
			_ = cmd.Process.Kill()
			if !*waiting {
				_ = cmd.Wait()
			}
		}()
		reader := bufio.NewReader(output)
		if ready, err := reader.ReadString('\n'); err != nil || ready != "READY\n" {
			t.Fatalf("child not waiting at common barrier: %q %v", ready, err)
		}
		peers = append(peers, peer{cmd, input, reader, waiting})
	}
	type result struct {
		text string
		err  error
	}
	results := make(chan result, 2)
	for _, p := range peers {
		*p.waiting = true
		go func() {
			_, writeErr := p.in.Write([]byte{1})
			_ = p.in.Close()
			raw, readErr := io.ReadAll(p.out)
			waitErr := p.command.Wait()
			if writeErr != nil {
				results <- result{err: writeErr}
				return
			}
			if readErr != nil {
				results <- result{err: readErr}
				return
			}
			results <- result{string(raw), waitErr}
		}()
	}
	counts := map[string]int{}
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("concurrent identity fixture: %v %s", got.err, got.text)
		}
		counts[got.text]++
	}
	if counts["OWNED"] != 1 || counts["DENIED"] != 1 {
		t.Fatalf("exclusive identity admitted duplicate or lost both: %v", counts)
	}
}
