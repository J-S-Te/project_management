package application

import (
	"testing"
	"time"
)

// 全程使用（时段留空）时，设备检定校验必须以计划开始日为边界；
// 用零值时间比较会把“检定从计划开始日生效”的有效设备误判为越界。
func TestEffectiveWindowStartUsesPlanStartForFullWindow(t *testing.T) {
	planStart := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if got := effectiveWindowStart(time.Time{}, planStart); !got.Equal(planStart) {
		t.Fatalf("full window must validate against plan start, got %s", got)
	}
	windowStart := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	if got := effectiveWindowStart(windowStart, planStart); !got.Equal(windowStart) {
		t.Fatalf("explicit window must be kept, got %s", got)
	}
}
