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
	for _, d := range s.AllowDests {
		rules = append(rules, destMatch(d))
	}
	rules = append(rules, []expr.Any{rejectAdmin()})
	return rules
}

// destMatch accepts packets to exactly one service endpoint, any UID.
func destMatch(d Dest) []expr.Any {
	proto := byte(unix.IPPROTO_TCP)
	if d.Proto == "udp" {
		proto = unix.IPPROTO_UDP
	}
	addr := d.Addr.As4()
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr[:]},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(d.Port)},
		accept(),
	}
}

// ctDirReply is IP_CT_DIR_REPLY.
const ctDirReply = 1

// Apply installs (or replaces) the fence in the current network namespace.
// Idempotent and race-safe: the old table is deleted in the same netlink
// batch that creates the new one, so there is no window in which the
// namespace is unfenced, and concurrent applies cannot stack rule sets.
func Apply(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("egressfence: netlink: %w", err)
	}
	// add-delete-add in ONE batch: the first add makes sure there is a table
	// to delete (a non-exclusive add of an existing table is a no-op), the
	// delete drops whatever is there, the second add starts clean. The batch
	// commits atomically, so two helpers racing on the same namespace each
	// leave exactly one fresh table — never one with both rule sets appended.
	c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: TableName})
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: TableName})
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
// with a drop policy and exactly the rules Apply writes for s, in order —
// each rule's per-rule marker AND its expressions as read back from the
// kernel.
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
	want := Rules(s)
	st.Valid = chainOK && len(rules) == len(want)
	for i := 0; st.Valid && i < len(rules); i++ {
		// The marker says which rule this claims to be; the expressions say
		// what it does. Both must match: a rule edited in place keeps its
		// marker.
		st.Valid = string(rules[i].UserData) == s.marker(i) && sameExprs(rules[i].Exprs, want[i])
	}
	return st, nil
}

// sameExprs compares a rule read back from the kernel with the rule Apply
// writes. Compared as rendered values so a field the decoder fills in with
// its zero value on one side and leaves unset on the other cannot differ.
func sameExprs(got, want []expr.Any) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if fmt.Sprintf("%T%+v", got[i], got[i]) != fmt.Sprintf("%T%+v", want[i], want[i]) {
			return false
		}
	}
	return true
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
