package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// importUploadRequestIDPrefix 是本子系统导入源文件幂等键的固定前缀，便于网关侧按来源检索。
const importUploadRequestIDPrefix = "PMIMP"

// importUploadRequestIDHexLength 截取 SHA-256 十六进制摘要的前 40 位：
// 160 bit 足以避免同一租户/应用内的键冲突，同时把键长压在网关上限制内。
const importUploadRequestIDHexLength = 40

// importUploadRequestID 生成导入源文件上传的确定性幂等键。
//
// 预览与确认复用同一个读取函数，同一份文件（类型、文件名、内容都相同）必须得到同一个键，
// 这样网关按 (tenant, application, idempotency_key) 幂等，不会把同一份 CSV 重复落成多份文件记录。
// 键的输入包含导入类型、文件名与内容摘要，任何一个变化都会产生新键，避免不同文件误命中同一会话。
//
// 键形如 PMIMP-CAPABILITY-<40 位大写十六进制>，仅含大写字母、数字和连字符，
// 长度远小于网关会话表 idempotency_key 的 255 字符上限。
func importUploadRequestID(importType, fileName string, content []byte) string {
	digest := sha256.New()
	digest.Write([]byte(importType))
	digest.Write([]byte{0})
	digest.Write([]byte(fileName))
	digest.Write([]byte{0})
	digest.Write(content)
	sum := hex.EncodeToString(digest.Sum(nil))
	if len(sum) > importUploadRequestIDHexLength {
		sum = sum[:importUploadRequestIDHexLength]
	}
	// 键统一大写，落在网关键字符集（字母、数字、连字符）内，便于与请求头 X-Request-ID 区分。
	sum = strings.ToUpper(sum)
	return importUploadRequestIDPrefix + "-" + importUploadKeyTypeSegment(importType) + "-" + sum
}

// importUploadKeyTypeSegment 把导入类型规范成幂等键片段：大写，非字母数字一律替换为连字符。
// 例如 DETECTION_CATEGORY 变成 DETECTION-CATEGORY，避免下划线等字符落在网关键字符集之外。
func importUploadKeyTypeSegment(importType string) string {
	segment := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(importType))
	segment = strings.Trim(segment, "-")
	if segment == "" {
		return "IMPORT"
	}
	return segment
}
