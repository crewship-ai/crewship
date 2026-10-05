package backup

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

const productionKDFProbeArg = "--crewship-production-kdf-probe"

func TestMain(m *testing.M) {
	// The child exercises package initialization before the test-only override.
	// This argument exists only in the test binary, never in the shipped CLI.
	if len(os.Args) == 2 && os.Args[1] == productionKDFProbeArg {
		w, err := EncryptStreamPassphrase(os.Stdout, "production-default-probe")
		if err == nil {
			_, err = io.WriteString(w, "production AGE compatibility probe")
		}
		if err == nil {
			err = w.Close()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	// Immutable throughout test execution, including parallel tests. The
	// decryptor reads the factor from each AGE stanza, so roundtrips still
	// exercise the real encryption format and authentication behavior.
	passphraseWorkFactor = 10
	os.Exit(m.Run())
}

func TestProductionPassphraseWorkFactor(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, productionKDFProbeArg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	ciphertext, err := cmd.Output()
	if err != nil {
		t.Fatalf("production KDF probe: %v\n%s", err, &stderr)
	}
	scanner := bufio.NewScanner(bytes.NewReader(ciphertext))
	if !scanner.Scan() || scanner.Text() != "age-encryption.org/v1" {
		t.Fatal("production encryption did not emit an AGE v1 header")
	}
	if !scanner.Scan() {
		t.Fatal("production encryption did not emit a recipient stanza")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) != 4 || fields[0] != "->" || fields[1] != "scrypt" {
		t.Fatalf("production recipient stanza: %q", scanner.Text())
	}
	factor, err := strconv.Atoi(fields[3])
	if err != nil || factor < 18 {
		t.Fatalf("production scrypt factor %q is below 18: %v", fields[3], err)
	}
}
