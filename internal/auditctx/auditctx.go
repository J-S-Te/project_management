// Package auditctx 携带请求级审计来源信息（AUD-IP-2026）。
package auditctx

import "context"

// Meta 是本次请求的登录地址、操作地址与来源标记。
type Meta struct {
	LoginIP, ClientIP, IPSource, RequestID string
	// ActorID 是当前请求的稳定操作者 ID；仓储层在缺少显式入参时用它写审计。
	ActorID string
}

type metaKey struct{}

func WithMeta(ctx context.Context, meta Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, meta)
}

func FromContext(ctx context.Context) Meta {
	meta, _ := ctx.Value(metaKey{}).(Meta)
	return meta
}
