package temporalworker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.temporal.io/sdk/client"
)

// MetricsRegistry 接收 Temporal SDK 指标并以 Prometheus 文本格式暴露。
// 标签只来自 SDK 的任务队列、Workflow 和 Activity 类型，禁止业务输入形成高基数标签。
type MetricsRegistry struct {
	mu       sync.RWMutex
	counters map[string]float64
	gauges   map[string]float64
	timers   map[string]timerValue
	tags     map[string]string
}

type timerValue struct{ Count, Sum float64 }

// NewMetricsRegistry 创建项目 Worker 私有的进程内指标注册表。
func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{counters: map[string]float64{}, gauges: map[string]float64{}, timers: map[string]timerValue{}, tags: map[string]string{}}
}

// WithTags 返回共享底层存储并合并固定标签的指标视图。
func (registry *MetricsRegistry) WithTags(tags map[string]string) client.MetricsHandler {
	return &taggedMetrics{root: registry, tags: mergeTags(registry.tags, tags)}
}

// Counter 返回累计计数器。
func (registry *MetricsRegistry) Counter(name string) client.MetricsCounter {
	return metricCounter{root: registry, key: metricKey(name, registry.tags)}
}

// Gauge 返回瞬时值指标。
func (registry *MetricsRegistry) Gauge(name string) client.MetricsGauge {
	return metricGauge{root: registry, key: metricKey(name, registry.tags)}
}

// Timer 返回以秒为单位累计次数和耗时的计时器。
func (registry *MetricsRegistry) Timer(name string) client.MetricsTimer {
	return metricTimer{root: registry, key: metricKey(name, registry.tags)}
}

type taggedMetrics struct {
	root *MetricsRegistry
	tags map[string]string
}

func (handler *taggedMetrics) WithTags(tags map[string]string) client.MetricsHandler {
	return &taggedMetrics{root: handler.root, tags: mergeTags(handler.tags, tags)}
}
func (handler *taggedMetrics) Counter(name string) client.MetricsCounter {
	return metricCounter{root: handler.root, key: metricKey(name, handler.tags)}
}
func (handler *taggedMetrics) Gauge(name string) client.MetricsGauge {
	return metricGauge{root: handler.root, key: metricKey(name, handler.tags)}
}
func (handler *taggedMetrics) Timer(name string) client.MetricsTimer {
	return metricTimer{root: handler.root, key: metricKey(name, handler.tags)}
}

type metricCounter struct {
	root *MetricsRegistry
	key  string
}

func (counter metricCounter) Inc(delta int64) {
	counter.root.mu.Lock()
	counter.root.counters[counter.key] += float64(delta)
	counter.root.mu.Unlock()
}

type metricGauge struct {
	root *MetricsRegistry
	key  string
}

func (gauge metricGauge) Update(value float64) {
	gauge.root.mu.Lock()
	gauge.root.gauges[gauge.key] = value
	gauge.root.mu.Unlock()
}

type metricTimer struct {
	root *MetricsRegistry
	key  string
}

func (timer metricTimer) Record(duration time.Duration) {
	timer.root.mu.Lock()
	value := timer.root.timers[timer.key]
	value.Count++
	value.Sum += duration.Seconds()
	timer.root.timers[timer.key] = value
	timer.root.mu.Unlock()
}

// ServeHTTP 输出 Prometheus 文本；temporal_workflow_failed_total 是工作流失败告警指标。
func (registry *MetricsRegistry) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	keys := make([]string, 0, len(registry.counters)+len(registry.gauges)+len(registry.timers))
	for key := range registry.counters {
		keys = append(keys, "c\x00"+key)
	}
	for key := range registry.gauges {
		keys = append(keys, "g\x00"+key)
	}
	for key := range registry.timers {
		keys = append(keys, "t\x00"+key)
	}
	sort.Strings(keys)
	for _, encoded := range keys {
		kind, key := encoded[:1], encoded[2:]
		switch kind {
		case "c":
			name, labels := splitMetricKey(key)
			if !strings.HasSuffix(name, "_total") {
				name += "_total"
			}
			fmt.Fprintf(writer, "%s%s %s\n", name, labels, strconv.FormatFloat(registry.counters[key], 'f', -1, 64))
		case "g":
			fmt.Fprintf(writer, "%s %s\n", key, strconv.FormatFloat(registry.gauges[key], 'f', -1, 64))
		case "t":
			value := registry.timers[key]
			name, labels := splitMetricKey(key)
			fmt.Fprintf(writer, "%s_sum%s %s\n%s_count%s %s\n", name, labels, strconv.FormatFloat(value.Sum, 'f', -1, 64), name, labels, strconv.FormatFloat(value.Count, 'f', -1, 64))
		}
	}
}

// StartMetricsServer 启动独立指标端口，并在上下文取消时有界关闭。
func StartMetricsServer(ctx context.Context, address string, registry *MetricsRegistry, logger *slog.Logger) error {
	if registry == nil || logger == nil || strings.TrimSpace(address) == "" {
		return fmt.Errorf("Temporal metrics server requires address, registry and logger")
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", registry)
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			logger.Error("Temporal metrics server shutdown failed", "error", err)
		}
	}()
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Temporal metrics server failed", "error", err)
		}
	}()
	return nil
}

func metricKey(name string, tags map[string]string) string {
	name = sanitizeMetricName(name)
	if len(tags) == 0 {
		return name
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, sanitizeMetricName(key)+`="`+escapeLabel(tags[key])+`"`)
	}
	return name + "{" + strings.Join(parts, ",") + "}"
}
func splitMetricKey(key string) (string, string) {
	if index := strings.IndexByte(key, '{'); index >= 0 {
		return key[:index], key[index:]
	}
	return key, ""
}
func sanitizeMetricName(value string) string {
	var builder strings.Builder
	for index, character := range value {
		valid := character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || index > 0 && character >= '0' && character <= '9'
		if valid {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "temporal_metric"
	}
	return builder.String()
}
func escapeLabel(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
}
func mergeTags(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}

var _ client.MetricsHandler = (*MetricsRegistry)(nil)
