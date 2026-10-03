package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

func runOutputCommand(ctx context.Context, mode string, args []string, out, diagnostics io.Writer) int {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	dir := fs.String("dir", "", "private persistent run output directory")
	timeout := fs.Duration("timeout", 30*time.Minute, "execution or reader deadline")
	var argsFile *string
	var limit *int64
	var after *uint64
	var raw *bool
	if mode == "run-capture" {
		argsFile = fs.String("args-file", "", "NUL-separated command arguments (bounded to 1 MiB)")
		limit = fs.Int64("max-bytes", runoutput.DefaultLimit, "maximum committed output bytes, including terminal reserve")
	} else {
		after = fs.Uint64("after", 0, "last durably projected record sequence")
		raw = fs.Bool("raw", false, "emit command bytes instead of record envelopes")
	}
	if err := fs.Parse(args); err != nil {
		return 125
	}
	if *dir == "" || *timeout <= 0 {
		fmt.Fprintln(diagnostics, "run output directory and positive timeout are required")
		return 125
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if mode == "run-capture" {
		command := fs.Args()
		if *argsFile != "" {
			if len(command) > 0 {
				fmt.Fprintln(diagnostics, "choose command arguments or args-file")
				return 125
			}
			f, err := os.Open(*argsFile)
			if err != nil {
				fmt.Fprintln(diagnostics, err)
				return 125
			}
			data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
			f.Close()
			if err != nil || len(data) == 0 || len(data) > 1<<20 || data[len(data)-1] != 0 {
				fmt.Fprintln(diagnostics, "invalid bounded NUL-separated command file")
				return 125
			}
			for _, part := range bytes.Split(data[:len(data)-1], []byte{0}) {
				command = append(command, string(part))
			}
		}
		result, err := runoutput.Capture(ctx, *dir, command, *limit)
		if err != nil {
			fmt.Fprintln(diagnostics, err)
			return 125
		}
		if result.Reason != "exited" || result.ExitCode < 0 || result.ExitCode > 255 {
			return 125
		}
		return result.ExitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(diagnostics, "unexpected reader arguments")
		return 125
	}
	code := 125
	encoder := json.NewEncoder(out)
	err := runoutput.Follow(ctx, *dir, *after, func(record runoutput.Record) error {
		if record.Kind == "exit" && record.Reason == "exited" && record.ExitCode >= 0 && record.ExitCode <= 255 {
			code = record.ExitCode
		}
		if *raw {
			if record.Kind == "output" {
				_, err := out.Write(record.Data)
				return err
			}
			return nil
		}
		return encoder.Encode(record)
	})
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 125
	}
	if *raw {
		return code
	}
	return 0 // Transport success; the exit record carries the command result.
}
