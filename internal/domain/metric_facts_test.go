package domain

import (
	"testing"
	"time"
)

// TestMetricIntervalsBoundaries 验证北京时间日边界、半开相邻和零容量不推算。
// 参数：t 为测试上下文。
// 返回值：无；越界、非整分钟或重叠被接受时失败。
func TestMetricIntervalsBoundaries(t *testing.T) {
	at := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		rows    []MetricInterval
		want    int64
		invalid bool
	}{
		{"explicit zero", nil, 0, false},
		{"adjacent", []MetricInterval{{at, at.Add(time.Hour)}, {at.Add(time.Hour), at.Add(2 * time.Hour)}}, 120, false},
		{"overlap", []MetricInterval{{at, at.Add(time.Hour)}, {at.Add(30 * time.Minute), at.Add(2 * time.Hour)}}, 0, true},
		{"outside Beijing day", []MetricInterval{{at.Add(-time.Minute), at.Add(time.Hour)}}, 0, true},
		{"fractional minute", []MetricInterval{{at, at.Add(30 * time.Second)}}, 0, true},
		{"negative", []MetricInterval{{at.Add(time.Hour), at}}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateMetricIntervals("2026-11-01", tt.rows)
			if (err != nil) != tt.invalid || got != tt.want {
				t.Fatalf("got=%d err=%v", got, err)
			}
		})
	}
	if MetricContains([]MetricInterval{{at, at.Add(time.Hour)}, {at.Add(2 * time.Hour), at.Add(3 * time.Hour)}}, MetricInterval{at, at.Add(3 * time.Hour)}) {
		t.Fatal("capacity gap treated as available")
	}
	if !MetricContains([]MetricInterval{{at, at.Add(time.Hour)}, {at.Add(time.Hour), at.Add(2 * time.Hour)}}, MetricInterval{at, at.Add(2 * time.Hour)}) {
		t.Fatal("adjacent capacity should cover interval")
	}
}
