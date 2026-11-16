package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// MetricUsageCandidates 从服务项现场事件推算人员用量候选，不写事实。
// 参数：ctx/p 为真实读取请求，itemID 为已授权服务项。
// 返回值：按业务日排序的候选；计划、预约和单独签到仍不会自动成为实际用量。
func (s *Service) MetricUsageCandidates(ctx context.Context, p platform.Principal, itemID string) ([]domain.MetricUsageCandidate, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return nil, err
	}
	repo, ok := s.Repo.(DeliveryRepository)
	if !ok {
		return nil, ErrProjectSnapshotUnavailable
	}
	events, err := repo.ListDeliveryEvents(ctx, filter, item.ProjectID)
	if err != nil {
		return nil, err
	}
	capabilities, err := repo.ListCapabilities(ctx, p.TenantID, "PERSON")
	if err != nil {
		return nil, err
	}
	capabilityByUser := make(map[string]domain.Capability, len(capabilities))
	for _, capability := range capabilities {
		if capability.UserID != "" && capability.Status == "ACTIVE" {
			if _, exists := capabilityByUser[capability.UserID]; !exists {
				capabilityByUser[capability.UserID] = capability
			}
		}
	}
	type group struct {
		start, end                 time.Time
		checkin, record, signature bool
		eventIDs                   []string
	}
	groups := make(map[string]*group)
	for _, event := range events {
		if event.ServiceItemID != itemID || event.ActorUserID == "" {
			continue
		}
		at := event.CreatedAt
		if event.CapturedAt != nil {
			at = *event.CapturedAt
		}
		if at.IsZero() {
			continue
		}
		key := event.ActorUserID + "|" + metricBusinessDate(at)
		current := groups[key]
		if current == nil {
			current = &group{}
			groups[key] = current
		}
		current.eventIDs = append(current.eventIDs, event.ID)
		switch event.Type {
		case EventFieldStarted, EventFieldCheckedIn:
			if current.start.IsZero() || at.Before(current.start) {
				current.start = at
			}
			if event.Type == EventFieldCheckedIn {
				current.checkin = true
			}
		case EventFieldRecordSubmitted:
			current.record = true
			if at.After(current.end) {
				current.end = at
			}
		case EventFieldSignatureCaptured:
			current.signature = true
			if at.After(current.end) {
				current.end = at
			}
		case EventFieldCompleted:
			if at.After(current.end) {
				current.end = at
			}
		}
	}
	enabled := false
	if store, storeErr := metricCalendarStore(s); storeErr == nil {
		if setting, settingErr := store.GetMetricAutoConfirmSetting(ctx, p.TenantID); settingErr == nil {
			enabled = setting.Enabled
		}
	}
	out := make([]domain.MetricUsageCandidate, 0, len(groups))
	for key, current := range groups {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 || current.start.IsZero() {
			continue
		}
		actor := parts[0]
		capability := capabilityByUser[actor]
		candidate := domain.MetricUsageCandidate{ServiceItemID: itemID, ResourceType: "PERSON", ResourceID: capability.ResourceID, IdentityID: actor, BusinessDate: parts[1], Start: current.start, Source: "FIELD_EVENTS", SourceEventIDs: uniqueMetricIDs(current.eventIDs)}
		// 现场采集包含秒和毫秒，仅核算事件明确覆盖的完整分钟，不向外扩张实际时段。
		candidate.Start = current.start.Truncate(time.Minute)
		if candidate.Start.Before(current.start) {
			candidate.Start = candidate.Start.Add(time.Minute)
		}
		if capability.ResourceID == "" {
			candidate.Reason = "未找到该现场人员的有效能力档案"
		}
		if !current.end.IsZero() {
			candidate.End = current.end.Truncate(time.Minute)
			if candidate.End.After(candidate.Start) {
				candidate.Minutes = int64(candidate.End.Sub(candidate.Start) / time.Minute)
			}
		}
		sameDay := !current.end.IsZero() && metricBusinessDate(current.end) == candidate.BusinessDate
		candidate.Complete = capability.ResourceID != "" && current.checkin && (current.record || current.signature) && sameDay && candidate.Minutes > 0
		candidate.AutoConfirmable = enabled && candidate.Complete
		if candidate.Reason == "" && !candidate.Complete {
			switch {
			case !sameDay && !current.end.IsZero():
				candidate.Reason = "现场事件跨业务日，请拆分或人工登记"
			case current.end.IsZero():
				candidate.Reason = "缺少现场记录/签名结束事件"
			case !current.checkin:
				candidate.Reason = "缺少 GPS 签到事件"
			default:
				candidate.Reason = "事件链不完整"
			}
		}
		var evidence []string
		if !current.start.IsZero() {
			evidence = append(evidence, "现场开始/签到")
		}
		if current.record {
			evidence = append(evidence, "现场记录")
		}
		if current.signature {
			evidence = append(evidence, "电子签名")
		}
		candidate.Evidence = strings.Join(evidence, " + ") + "（按完整分钟生成候选，待核对）"
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BusinessDate != out[j].BusinessDate {
			return out[i].BusinessDate < out[j].BusinessDate
		}
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].ResourceID < out[j].ResourceID
	})
	return out, nil
}

// ApplyMetricUsageCandidate 重新计算候选并登记；策略开启且事件链完整时直接强证据确认。
// 参数：ctx/p 为真实命令，itemID 为服务项，in 为资源、业务日和幂等键。
// 返回值：原命令精确回执或新登记；新命令缺证据/容量或原键绑定不符时拒绝。
func (s *Service) ApplyMetricUsageCandidate(ctx context.Context, p platform.Principal, itemID string, in domain.MetricUsageCandidateApplyInput) (domain.ResourceUsage, error) {
	if itemID == "" || in.ResourceID == "" || !validMetricDate(in.BusinessDate) || !metricKeyValid(in.IdempotencyKey) {
		return domain.ResourceUsage{}, ErrValidation
	}
	prior, found, err := s.replayMetricUsageCandidate(ctx, p, itemID, in)
	if err != nil || found {
		return prior, err
	}
	candidates, err := s.MetricUsageCandidates(ctx, p, itemID)
	if err != nil {
		return domain.ResourceUsage{}, err
	}
	var candidate *domain.MetricUsageCandidate
	for index := range candidates {
		if candidates[index].ResourceID == in.ResourceID && candidates[index].BusinessDate == in.BusinessDate {
			candidate = &candidates[index]
			break
		}
	}
	if candidate == nil {
		return domain.ResourceUsage{}, ErrNotFound
	}
	if len(candidate.SourceEventIDs) == 0 || candidate.End.IsZero() {
		return domain.ResourceUsage{}, ValidationError("该候选缺少完整现场事件，不能自动登记")
	}
	autoConfirm := false
	store, storeErr := metricCalendarStore(s)
	if storeErr == nil {
		if setting, settingErr := store.GetMetricAutoConfirmSetting(ctx, p.TenantID); settingErr == nil && setting.Enabled && candidate.Complete {
			autoConfirm = true
		}
	}
	evidence := candidate.Evidence
	return s.createMetricUsage(ctx, p, domain.ResourceUsageInput{
		ResourceType: candidate.ResourceType, ResourceID: candidate.ResourceID,
		ServiceItemID: itemID, BusinessDate: candidate.BusinessDate,
		Start: candidate.Start, End: candidate.End,
		Note: "现场事件自动候选", Evidence: evidence,
		Source: "FIELD_EVENTS", SourceRef: metricCandidateRef(candidate.SourceEventIDs),
		SourceEventIDs: candidate.SourceEventIDs, AutoConfirm: autoConfirm,
		IdempotencyKey: in.IdempotencyKey,
	}, true)
}

func uniqueMetricIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func metricCandidateRef(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return "field:" + hex.EncodeToString(sum[:])
}

func metricBusinessDate(at time.Time) string {
	return at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
}
