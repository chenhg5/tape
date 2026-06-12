package scan

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

// UUIDv4 returns a random RFC 4122 v4 UUID.
func UUIDv4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return format(b)
}

// UUIDv7 returns a time-ordered v7 UUID (what Codex uses for session ids).
func UUIDv7() string {
	var b [16]byte
	rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(b[:8], ms<<16|uint64(binary.BigEndian.Uint16(b[6:8])))
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return format(b)
}

func format(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
