package httpapi

import (
	"bufio"
	"bytes"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

// maxAuditBufferBytes 是强制审计模式下允许缓冲的响应体上限（对齐 platform 修复口径的 64KiB）。
// 超过上限的响应（大体积流式导出等）降级为 write-through：响应照常送达客户端，
// 但审计失败无法再用 503 撤回数据，只能靠日志事后追溯（取舍见 auditWrites 的注释）。
const maxAuditBufferBytes = 64 * 1024

// auditBuffer 缓冲强制审计模式下不安全方法（写请求）与敏感读（导出/下载/令牌签发）的响应。
//
// 安全理由（SEC-D4b / AUD-2026-006）：gin 的审计中间件在业务 handler 执行完毕后才能拿到
// Report 的结果，而此时业务响应往往已经写给客户端、无法撤回。只有先把响应
// 缓冲在内存里，才能在审计事件写入失败时丢弃业务响应并返回 503，保证
// “审计写不进去 ⇒ 客户端拿不到成功”，杜绝强制审计模式下的静默无审计写入。
// 仅在 PLATFORM_AUDIT_REQUIRED/prod 强制模式下启用，非强制模式行为不变。
type auditBuffer struct {
	orig    gin.ResponseWriter
	headers http.Header
	body    bytes.Buffer
	status  int
	size    int
	// degraded 表示响应体超过 maxAuditBufferBytes 后已切换为 write-through：
	// 后续写入直接到达真实连接，flushTo 不再重复提交。
	degraded bool
}

func newAuditBuffer(orig gin.ResponseWriter) *auditBuffer {
	return &auditBuffer{orig: orig, headers: make(http.Header), status: http.StatusOK, size: -1}
}

// Degraded 报告该响应是否已超限降级为 write-through。
func (b *auditBuffer) Degraded() bool { return b.degraded }

// degrade 把已缓冲的响应头、状态码与响应体提交到真实连接，并把后续写入直通。
// 降级后审计失败只能记日志，无法撤回已经发出的数据。
func (b *auditBuffer) degrade() {
	if b.degraded {
		return
	}
	b.degraded = true
	b.commitTo(b.orig)
}

// commitTo 把缓冲的响应头、状态码与响应体提交到目标 writer，语义与 gin 自带
// responseWriter 一致。
func (b *auditBuffer) commitTo(dst gin.ResponseWriter) {
	b.WriteHeaderNow()
	for key, values := range b.headers {
		dst.Header().Del(key)
		for _, value := range values {
			dst.Header().Add(key, value)
		}
	}
	dst.WriteHeader(b.status)
	if b.body.Len() > 0 {
		_, _ = dst.Write(b.body.Bytes())
		return
	}
	dst.WriteHeaderNow()
}

// flushTo 在审计上报成功后把缓冲的响应提交到真实连接。
// 已降级（write-through）的响应早已提交，这里不再重复写出。
func (b *auditBuffer) flushTo(dst gin.ResponseWriter) {
	if b.degraded {
		return
	}
	b.commitTo(dst)
}

// —— 以下实现 gin.ResponseWriter 接口，行为对齐 gin 的 responseWriter。 ——

func (b *auditBuffer) Header() http.Header { return b.headers }

func (b *auditBuffer) WriteHeader(code int) {
	if code > 0 && b.status != code && !b.Written() {
		b.status = code
	}
}

func (b *auditBuffer) WriteHeaderNow() {
	if !b.Written() {
		b.size = 0
	}
}

func (b *auditBuffer) Write(p []byte) (int, error) {
	b.WriteHeaderNow()
	if b.degraded {
		n, err := b.orig.Write(p)
		b.size += n
		return n, err
	}
	n, err := b.body.Write(p)
	b.size += n
	if b.body.Len() > maxAuditBufferBytes {
		// 超限：把已缓冲内容（含本次写入）提交到真实连接，之后全部 write-through。
		b.degrade()
	}
	return n, err
}

func (b *auditBuffer) WriteString(s string) (int, error) {
	b.WriteHeaderNow()
	if b.degraded {
		n, err := b.orig.WriteString(s)
		b.size += n
		return n, err
	}
	n, err := b.body.WriteString(s)
	b.size += n
	if b.body.Len() > maxAuditBufferBytes {
		b.degrade()
	}
	return n, err
}

func (b *auditBuffer) Status() int   { return b.status }
func (b *auditBuffer) Size() int     { return b.size }
func (b *auditBuffer) Written() bool { return b.size != -1 }

// Flush 只标记“已写”，不能把半成品响应提前提交到真实连接，
// 否则审计失败时将无法撤回，拒绝语义会被破坏。
// 已降级为 write-through 后响应已在流式发送，Flush 透传到真实连接。
func (b *auditBuffer) Flush() {
	if b.degraded {
		b.orig.Flush()
		return
	}
	b.WriteHeaderNow()
}

func (b *auditBuffer) Pusher() http.Pusher { return b.orig.Pusher() }

func (b *auditBuffer) Hijack() (net.Conn, *bufio.ReadWriter, error) { return b.orig.Hijack() }

func (b *auditBuffer) CloseNotify() <-chan bool { return b.orig.CloseNotify() }

func (b *auditBuffer) Unwrap() http.ResponseWriter { return b.orig }
