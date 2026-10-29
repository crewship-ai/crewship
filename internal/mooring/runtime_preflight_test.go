package mooring

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRPCResourceBoundsApplyBeforeSyntaxParsing(t *testing.T) {
	for name, raw := range map[string][]byte{
		"deep": []byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)),
		"wide": []byte("[" + strings.Repeat("0,", 32768) + "0]"),
	} {
		t.Run(name, func(t *testing.T) {
			parent, child := net.Pipe()
			defer parent.Close()
			defer child.Close()
			rpc := &RPC{conn: parent, maxFrame: MaxPrivatePrepareRPCFrame}
			_ = parent.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := rpc.Write(raw); !errors.Is(err, ErrRPCFrame) {
				t.Fatal("write did not bound JSON resources", err)
			}
			_ = parent.SetWriteDeadline(time.Time{})
			go func() {
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
				_ = writeRPCBytes(child, header[:])
				_ = writeRPCBytes(child, raw)
			}()
			if _, err := rpc.Read(); !errors.Is(err, ErrRPCFrame) {
				t.Fatal("read did not bound JSON resources", err)
			}
		})
	}
	for _, raw := range [][]byte{
		[]byte(strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64)),
		[]byte("[" + strings.Repeat("0,", 32767) + "0]"),
	} {
		if !rpcJSONPreflight(raw) || !json.Valid(raw) {
			t.Fatal("exact structural bound refused")
		}
	}
	quoted, err := json.Marshal(strings.Repeat(`\"{[,:`, 20000))
	if err != nil || !rpcJSONPreflight(quoted) {
		t.Fatal("quoted structural characters counted as tokens", err)
	}
}
