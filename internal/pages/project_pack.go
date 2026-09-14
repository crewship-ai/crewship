package pages

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // Git pack checksum, not the source integrity boundary.
	"encoding/binary"
	"sort"
)

// Write one self-contained, undeltified pack instead of syncing hundreds of
// individual loose files. Only locally validated source/tree bytes enter this
// encoder; backup imports still validate and write individual objects.
// Format: https://git-scm.com/docs/gitformat-pack
// Git verifies/indexes the pack and durably installs it before the commit is pinned.
func writeProjectGitPack(ctx context.Context, repo string, objects projectGitObjects) error {
	var pack bytes.Buffer
	pack.WriteString("PACK")
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], 2)
	binary.BigEndian.PutUint32(header[4:], uint32(len(objects)))
	pack.Write(header[:])
	ids := make([]string, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		object := objects[id]
		typeCode := byte(3)
		if object.kind == "tree" {
			typeCode = 2
		}
		size := len(object.data)
		first := typeCode<<4 | byte(size&15)
		size >>= 4
		if size != 0 {
			first |= 128
		}
		pack.WriteByte(first)
		for size != 0 {
			next := byte(size & 127)
			size >>= 7
			if size != 0 {
				next |= 128
			}
			pack.WriteByte(next)
		}
		writer := zlib.NewWriter(&pack)
		if _, err := writer.Write(object.data); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
	}
	checksum := sha1.Sum(pack.Bytes())
	pack.Write(checksum[:])
	_, err := runProjectGit(ctx, repo, pack.Bytes(), "-c", "core.fsync=pack,pack-metadata", "index-pack", "--stdin", "--strict", "--threads=1")
	return err
}
