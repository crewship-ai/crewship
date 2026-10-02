//go:build linux

package egressfence

import (
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Rules returns the fence's rule expressions in order. Exported for tests so
// the rule set can be asserted without a kernel.
func Rules(s Spec) [][]expr.Any {
	var rules [][]expr.Any
	for _, uid := range s.AllowUIDs {
		rules = append(rules, append(dockerDNSMatch(), skuidEq(uid)...))
		rules[len(rules)-1] = append(rules[len(rules)-1], accept())
	}
	rules = append(rules, append(dockerDNSMatch(), rejectAdmin()))
	rules = append(rules, []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname("lo")},
		accept(),
	})
	// Replies to connections someone else opened TO this container. The
	// direction match is the point: a connection the agent itself opened
	// (original direction) is not let through on the strength of being
	// established — including one opened before the fence went in.
	rules = append(rules, []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeyDIRECTION},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{ctDirReply}},
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:            binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
		accept(),
	})
	for _, uid := range s.AllowUIDs {
		rules = append(rules, append(skuidEq(uid), accept()))
	}
	rules = append(rules, []expr.Any{rejectAdmin()})
	return rules
}

// ctDirReply is IP_CT_DIR_REPLY.
const ctDirReply = 1

// Apply installs (or replaces) the fence in the current network namespace.
// Idempotent: an existing crewship_fence table is deleted in the same netlink
// batch, so there is no window in which the namespace is unfenced between the
// old and the new rule set.
func Apply(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("egressfence: netlink: %w", err)
	}
	present, err := tablePresent(c)
	if err != nil {
		return err
	}
	if present {
		c.DelTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: TableName})
	}
	table := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: TableName})
	policy := nftables.ChainPolicyDrop
	chain := c.AddChain(&nftables.Chain{
		Name:     ChainName,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &policy,
	})
	for i, exprs := range Rules(s) {
		c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs, UserData: []byte(s.marker(i))})
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("egressfence: install: %w", err)
	}
	return nil
}

// Check reports whether the fence for s is installed in the current
// namespace, and whether what is installed is that fence: the output hook
// with a drop policy and exactly the rules Apply writes for s, in order,
// identified by the per-rule marker Apply stores in each rule's user data.
func Check(s Spec) (State, error) {
	c, err := nftables.New()
	if err != nil {
		return State{}, fmt.Errorf("egressfence: netlink: %w", err)
	}
	present, err := tablePresent(c)
	if err != nil || !present {
		return State{}, err
	}
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: TableName}
	chains, err := c.ListChainsOfTableFamily(nftables.TableFamilyINet)
	if err != nil {
		return State{}, fmt.Errorf("egressfence: list chains: %w", err)
	}
	var chainOK bool
	for _, ch := range chains {
		if ch.Table.Name == TableName && ch.Name == ChainName {
			chainOK = ch.Hooknum != nil && *ch.Hooknum == *nftables.ChainHookOutput &&
				ch.Policy != nil && *ch.Policy == nftables.ChainPolicyDrop
		}
	}
	rules, err := c.GetRules(table, &nftables.Chain{Name: ChainName, Table: table})
	if err != nil {
		return State{}, fmt.Errorf("egressfence: list rules: %w", err)
	}
	st := State{Present: true, Rules: len(rules)}
	want := len(Rules(s))
	st.Valid = chainOK && len(rules) == want
	for i := 0; st.Valid && i < len(rules); i++ {
		st.Valid = string(rules[i].UserData) == s.marker(i)
	}
	return st, nil
}

func tablePresent(c *nftables.Conn) (bool, error) {
	tables, err := c.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return false, fmt.Errorf("egressfence: list tables: %w", err)
	}
	for _, t := range tables {
		if t.Name == TableName {
			return true, nil
		}
	}
	return false, nil
}

func dockerDNSMatch() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(DockerDNS)},
	}
}

func skuidEq(uid uint32) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeySKUID, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uid)},
	}
}

func accept() expr.Any { return &expr.Verdict{Kind: expr.VerdictAccept} }

func rejectAdmin() expr.Any {
	return &expr.Reject{Type: unix.NFT_REJECT_ICMPX_UNREACH, Code: unix.NFT_REJECT_ICMPX_ADMIN_PROHIBITED}
}

func ifname(n string) []byte {
	b := make([]byte, 16)
	copy(b, n)
	return b
}
