package domain

// MetricUsageBatchItem 是一条批量用量命令的目标及客户端读取到的版本。
// 版本仍由服务端逐条复核，批量不能替代乐观锁。
type MetricUsageBatchItem struct {
	ID              string `json:"id"`
	ExpectedVersion uint64 `json:"expected_version"`
}

// MetricUsageBatchInput 是原子批量提交、确认或取消用量的命令。
// Items 最多 20 条；同一批次要么全部生效，要么全部回滚。
type MetricUsageBatchInput struct {
	Items          []MetricUsageBatchItem `json:"items"`
	Action         string                 `json:"action"`
	Decision       string                 `json:"decision,omitempty"`
	Note           string                 `json:"note,omitempty"`
	IdempotencyKey string                 `json:"idempotency_key"`
}

// MetricUsageBatchResult 返回本批次逐条生效后的真实用量。
type MetricUsageBatchResult struct {
	Items []ResourceUsage `json:"items"`
}

// MetricUsageBatchMaxItems 是单次批量命令的硬上限，与资源事实单页上限一致。
const MetricUsageBatchMaxItems = 50
