package handover

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"time"
)

// NewID 生成 26 字符标识（时间前缀 + 加密随机后缀），供交接冻结与回执主键使用。
func NewID(now time.Time) (string, error) {
	milliseconds := now.UTC().UnixMilli()
	if milliseconds < 0 || milliseconds > 0xFFFFFFFFFFFF {
		return "", fmt.Errorf("handover id timestamp is out of range")
	}
	var raw [16]byte
	raw[0] = byte(milliseconds >> 40)
	raw[1] = byte(milliseconds >> 32)
	raw[2] = byte(milliseconds >> 24)
	raw[3] = byte(milliseconds >> 16)
	raw[4] = byte(milliseconds >> 8)
	raw[5] = byte(milliseconds)
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", fmt.Errorf("read handover id randomness: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])[:26], nil
}
