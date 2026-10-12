// crewship-launcher is the standalone, static native admission executable.
package main

import (
	"fmt"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"os"
)

func main() {
	if len(os.Args) < 4 || os.Args[1] != "--managed-launch" {
		fmt.Fprintln(os.Stderr, "usage: crewship-launcher --managed-launch DESCRIPTOR ARG...")
		os.Exit(126)
	}
	if err := managedlaunch.Launch(os.Args[2], os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
}
