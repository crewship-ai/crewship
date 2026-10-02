package writerlease

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLeaseAliasesAndRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for _, alias := range leaseTestAliases(dir, path) {
		if alias != path {
			var err error
			if filepath.Base(alias) == "symlink.db" {
				err = os.Symlink(path, alias)
			} else {
				err = os.Link(path, alias)
			}
			if err != nil {
				t.Fatalf("filesystem alias unavailable: %v", err)
			}
		}
		second, err := Acquire(alias)
		if second != nil {
			second.Close()
		}
		if !errors.Is(err, ErrHeld) {
			t.Fatalf("alias %s = %v, want held", alias, err)
		}
	}
	if err = first.Verify(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(first.Verify(), ErrLost) {
		t.Fatal("closed lease still verified")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("close removed lock anchor", err)
	}
	next, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
}

func TestLeaseProcessHelper(t *testing.T) {
	if os.Getenv("CREWSHIP_WRITER_LEASE_TEST_HELPER") != "1" {
		return
	}
	lease, err := Acquire(os.Getenv("CREWSHIP_WRITER_LEASE_TEST_PATH"))
	if err != nil {
		fmt.Fprintln(os.Stdout, "refused")
		os.Exit(3)
	}
	defer lease.Close()
	fmt.Fprintln(os.Stdout, "owned")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
}

func TestLeaseExcludesAnotherProcessAndSurvivesCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "process.db")
	command := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseProcessHelper$")
		cmd.Env = append(os.Environ(), "CREWSHIP_WRITER_LEASE_TEST_HELPER=1", "CREWSHIP_WRITER_LEASE_TEST_PATH="+path)
		return cmd
	}
	child := command()
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "owned\n" {
		t.Fatalf("child ownership: %q %v", line, err)
	}
	competing := command()
	output, err := competing.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || string(output) != "refused\n" {
		t.Fatalf("competing child: %q %v", output, err)
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	recovered, err := Acquire(path)
	if err != nil {
		t.Fatal("crash did not release ownership", err)
	}
	defer recovered.Close()
	if err = recovered.Verify(); err != nil {
		t.Fatal(err)
	}
}
