//go:build linux

package egressfence

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mdlayher/netlink"
	"github.com/mdlayher/netlink/nltest"
	"golang.org/x/sys/unix"
)

func TestCheckRejectsIncompleteAndChangingReads(t *testing.T) {
	valid := State{Present: true, Valid: true, Rules: 6}
	invalid := State{Present: true, Rules: 0}
	denied := errors.New("permission denied")
	for _, tc := range []struct {
		name        string
		generations []uint32
		states      []State
		readErr     error
		genErrAt    int
		want        State
		wantErr     bool
	}{
		{name: "stable valid", generations: []uint32{1, 1}, states: []State{valid}, want: valid},
		{name: "incomplete dump then valid at same generation", generations: []uint32{1, 1, 1, 1}, states: []State{invalid, valid}, want: valid},
		{name: "absent then valid", generations: []uint32{1, 1, 1, 1}, states: []State{{}, valid}, want: valid},
		{name: "discard torn positive", generations: []uint32{1, 2, 2, 2, 2, 2}, states: []State{valid, invalid, invalid}, want: invalid},
		{name: "high generation bits differ", generations: []uint32{1, 65537, 65537, 65537, 65537, 65537}, states: []State{valid, invalid, invalid}, want: invalid},
		{name: "replacement then stable", generations: []uint32{1, 2, 2, 2}, states: []State{invalid, valid}, want: valid},
		{name: "permanently tampered", generations: []uint32{1, 1, 1, 1, 1, 1}, states: []State{invalid, invalid, invalid}, want: invalid},
		{name: "permanently absent", generations: []uint32{1, 1, 1, 1, 1, 1}, states: []State{{}, {}, {}}, want: State{}},
		{name: "never stable", generations: []uint32{1, 2, 2, 3, 3, 4}, states: []State{valid, valid, valid}, wantErr: true},
		{name: "stable transport error", generations: []uint32{1, 1}, states: []State{{}}, readErr: denied, wantErr: true},
		{name: "generation read fails before state", genErrAt: 1, wantErr: true},
		{name: "generation read fails after positive", generations: []uint32{1}, states: []State{valid}, genErrAt: 2, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, reads := 0, 0
			got, err := readStableState(func() (uint32, error) {
				g++
				if g == tc.genErrAt {
					return 0, denied
				}
				if g > len(tc.generations) {
					t.Fatal("unexpected extra generation read")
				}
				return tc.generations[g-1], nil
			}, func() (State, error) {
				if reads >= len(tc.states) {
					t.Fatal("unexpected extra state read")
				}
				st := tc.states[reads]
				reads++
				return st, tc.readErr
			})
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("check = %+v, %v; want %+v, error=%v", got, err, tc.want, tc.wantErr)
			}
			if reads != len(tc.states) {
				t.Fatalf("read %d snapshots, want %d", reads, len(tc.states))
			}
			if tc.readErr != nil && !errors.Is(err, tc.readErr) {
				t.Fatalf("transport error lost: %v", err)
			}
		})
	}
}

func TestReadGenerationValidatesKernelReply(t *testing.T) {
	genType := netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES << 8) | unix.NFT_MSG_NEWGEN)
	attrs := func(id []byte) []byte {
		return append([]byte{unix.NFPROTO_UNSPEC, unix.NFNETLINK_V0, 0, 0}, nltest.MustMarshalAttributes([]netlink.Attribute{{Type: unix.NFTA_GEN_ID, Data: id}})...)
	}
	good := attrs([]byte{0x12, 0x34, 0x56, 0x78})
	for _, tc := range []struct {
		name    string
		typ     netlink.HeaderType
		data    []byte
		wantErr bool
	}{
		{"full32bit big endian", genType, good, false},
		{"wrong message", genType + 1, good, true},
		{"truncated header", genType, []byte{0}, true},
		{"missing generation", genType, []byte{0, 0, 0, 0}, true},
		{"short generation", genType, attrs([]byte{1}), true},
		{"duplicate generation", genType, append(append([]byte(nil), good...), good[4:]...), true},
		{"unknown attribute", genType, append(append([]byte(nil), good...), nltest.MustMarshalAttributes([]netlink.Attribute{{Type: 99, Data: []byte{1}}})...), false},
		{"malformed attribute", genType, []byte{0, 0, 0, 0, 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := nltest.Dial(func(req []netlink.Message) ([]netlink.Message, error) {
				if len(req) != 1 || req[0].Header.Type != netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_GETGEN) || !reflect.DeepEqual(req[0].Data, []byte{0, 0, 0, 0}) {
					t.Fatalf("unexpected generation request: %+v", req)
				}
				return []netlink.Message{{Header: netlink.Header{Type: tc.typ, Sequence: req[0].Header.Sequence, PID: req[0].Header.PID}, Data: tc.data}}, nil
			})
			defer func() { _ = c.Close() }()
			got, err := readGeneration(c)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got != 0x12345678) {
				t.Fatalf("generation = %x, %v", got, err)
			}
		})
	}
}

func TestReadGenerationPropagatesPermissionError(t *testing.T) {
	c := nltest.Dial(func(req []netlink.Message) ([]netlink.Message, error) {
		return nltest.Error(int(unix.EPERM), req)
	})
	defer func() { _ = c.Close() }()
	if _, err := readGeneration(c); !errors.Is(err, unix.EPERM) {
		t.Fatalf("permission error was lost: %v", err)
	}
}
