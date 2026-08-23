package store

import (
	"crypto/rand"
	"fmt"
)

// newUUID 生成 RFC 4122 v4 UUID（crypto/rand，避免引入外部依赖）。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 在 Linux 上几乎不会失败；失败即中止避免分发重复 id
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewID 供告警引擎等包生成事件/规则 ID。
func NewID() string { return newUUID() }
