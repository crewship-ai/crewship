//go:build linux

// fencetamper is a test-only program for TestEgressFenceIntegration: it turns
// the fence's final reject into an accept while keeping the rule's marker, so
// the test can prove Check reads rule contents and not just markers.
package main

import (
	"fmt"
	"os"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"

	"github.com/crewship-ai/crewship/internal/egressfence"
)

func main() {
	c, err := nftables.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: egressfence.TableName}
	chain := &nftables.Chain{Name: egressfence.ChainName, Table: table}
	rules, err := c.GetRules(table, chain)
	if err != nil || len(rules) == 0 {
		fmt.Fprintln(os.Stderr, "no fence rules", err)
		os.Exit(1)
	}
	last := rules[len(rules)-1]
	c.ReplaceRule(&nftables.Rule{
		Table:    table,
		Chain:    chain,
		Handle:   last.Handle,
		Exprs:    []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}},
		UserData: last.UserData,
	})
	if err := c.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
