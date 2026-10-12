//go:build linux

package egressfence

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

func checkStable(s Spec) (State, error) {
	c, err := netlink.Dial(unix.NETLINK_NETFILTER, nil)
	if err != nil {
		return State{}, fmt.Errorf("egressfence: generation socket: %w", err)
	}
	defer func() { _ = c.Close() }()
	return readStableState(func() (uint32, error) { return readGeneration(c) }, func() (State, error) { return checkOnce(s) })
}

// Rule dumps are separate reads, and the nftables library does not expose
// interrupted-dump flags. Reject positive reads spanning a generation change.
// Negative reads also need a bounded retry: during RCU table replacement,
// a dump can briefly see an old table without its active rules even when the
// generation counter already changed. No rule installation happens here.
func readStableState(generation func() (uint32, error), read func() (State, error)) (State, error) {
	const attempts = 3
	for attempt := 0; attempt < attempts; attempt++ {
		before, err := generation()
		if err != nil {
			return State{}, err
		}
		st, readErr := read()
		after, err := generation()
		if err != nil {
			return State{}, err
		}
		if before == after {
			if readErr != nil || (st.Present && st.Valid) || attempt == attempts-1 {
				return st, readErr
			}
		}
		if attempt < attempts-1 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return State{}, errors.New("egressfence: rules changed during every check")
}

func readGeneration(c *netlink.Conn) (uint32, error) {
	msgs, err := c.Execute(netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES << 8) | unix.NFT_MSG_GETGEN),
			Flags: netlink.Request,
		},
		Data: []byte{unix.NFPROTO_UNSPEC, unix.NFNETLINK_V0, 0, 0},
	})
	if err != nil {
		return 0, fmt.Errorf("egressfence: read generation: %w", err)
	}
	if len(msgs) != 1 || msgs[0].Header.Type != netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_NEWGEN) || len(msgs[0].Data) < 4 {
		return 0, errors.New("egressfence: malformed generation reply")
	}
	ad, err := netlink.NewAttributeDecoder(msgs[0].Data[4:])
	if err != nil {
		return 0, fmt.Errorf("egressfence: decode generation: %w", err)
	}
	ad.ByteOrder = binary.BigEndian
	var id uint32
	var found bool
	for ad.Next() {
		if ad.Type() == unix.NFTA_GEN_ID {
			if found {
				return 0, errors.New("egressfence: duplicate generation id")
			}
			id, found = ad.Uint32(), true
		}
	}
	if err := ad.Err(); err != nil {
		return 0, fmt.Errorf("egressfence: decode generation: %w", err)
	}
	if !found {
		return 0, errors.New("egressfence: generation id missing")
	}
	return id, nil
}
