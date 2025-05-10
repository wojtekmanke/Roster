// Package id generates prefixed, time-sortable identifiers such as
// "run_01jb2m5x8q4k7n9p3r6t0v2w4y".
package id

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"time"
)

// Crockford's alphabet, lowercased: no i, l, o or u, so IDs survive being read aloud.
var encoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// New returns an ID of the form prefix_<26 chars>. The first 48 bits are the
// current time in milliseconds, so IDs sort by creation time.
func New(prefix string) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("id: crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + encoding.EncodeToString(b[:])
}
