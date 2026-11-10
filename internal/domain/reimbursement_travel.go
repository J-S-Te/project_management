package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

const MaxExpenseTravelSourceDepth = 16

type ExpenseTravelSource struct {
	EventID       string `json:"event_id"`
	ServiceItemID string `json:"service_item_id"`
	EventVersion  string `json:"event_version"`
	ContentDigest string `json:"content_digest"`
}
type ExpenseTravelSnapshot struct {
	Status             string                `json:"status"`
	Mode               string                `json:"mode"`
	SourceEventID      string                `json:"source_event_id,omitempty"`
	SourceEventVersion string                `json:"source_event_version,omitempty"`
	ContentDigest      string                `json:"content_digest"`
	SourceChain        []ExpenseTravelSource `json:"source_chain"`
	Origin             string                `json:"origin,omitempty"`
	Destination        string                `json:"destination,omitempty"`
	DepartureDate      string                `json:"departure_date,omitempty"`
	ReturnDate         string                `json:"return_date,omitempty"`
	Transport          string                `json:"transport,omitempty"`
	AccommodationNeed  string                `json:"accommodation_need,omitempty"`
	ActorInTravelers   *bool                 `json:"actor_in_travelers"`
	SiteMatchStatus    string                `json:"site_match_status"`
	CityCodeStatus     string                `json:"city_code_status"`
	DateMatchStatus    string                `json:"date_match_status"`
	ReasonCodes        []string              `json:"reason_codes"`
}

// unknownExpenseTravel 建立明确未知的事实容器，不把服务项排期当作出差或审批。
// 参数：reason为未知原因；不包含人员或准备备注。
// 返回值：尚未完成内容摘要的最小未知快照。
func unknownExpenseTravel(reason string) ExpenseTravelSnapshot {
	return ExpenseTravelSnapshot{Status: "UNKNOWN", Mode: "UNKNOWN", SourceChain: []ExpenseTravelSource{}, SiteMatchStatus: "UNKNOWN", CityCodeStatus: "UNKNOWN", DateMatchStatus: "UNKNOWN", ReasonCodes: []string{reason}}
}

// ExpenseTravelArrangement 解码事件的真实行程字段，不使用裸行程编号猜测其他事件。
// 参数：event为不可变准备事件。
// 返回值：结构化行程及是否可解码；缺失或异常结构返回false。
func ExpenseTravelArrangement(event DeliveryEvent) (TravelArrangementInput, bool) {
	raw, err := json.Marshal(event.Payload["travel"])
	if err != nil || len(raw) > 32<<10 || string(raw) == "null" {
		return TravelArrangementInput{}, false
	}
	var travel TravelArrangementInput
	if json.Unmarshal(raw, &travel) != nil {
		return travel, false
	}
	travel.Mode = strings.ToUpper(strings.TrimSpace(travel.Mode))
	return travel, true
}

// ExpenseTravelEventSource 将不可变事件内容编码成真实内容版本，不伪造不存在的数字行版本。
// 参数：event为包含真实租户及项目服务项归属的准备事件。
// 返回值：事件标识、sha256内容版本与摘要；无法编码时摘要为空。
func ExpenseTravelEventSource(event DeliveryEvent) ExpenseTravelSource {
	value := struct {
		ID, TenantID, ProjectID, ServiceItemID, Type, ActorUserID string
		CreatedAt                                                 time.Time
		Payload                                                   map[string]any
	}{event.ID, event.TenantID, event.ProjectID, event.ServiceItemID, event.Type, event.ActorUserID, event.CreatedAt, event.Payload}
	raw, err := json.Marshal(value)
	if err != nil {
		return ExpenseTravelSource{}
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	return ExpenseTravelSource{EventID: event.ID, ServiceItemID: event.ServiceItemID, EventVersion: "sha256:" + digest, ContentDigest: digest}
}

// ExpenseTravelFacts 只沿显式同租户同项目准备事件链生成行程事实，循环或缺源保持未知。
// 参数：p/item为权威关联，actor为当前人员，occurredDate为费用日，current/sources为精确事件事实。
// 返回值：最小行程快照；不包含其他参与人列表，不表示差旅审批通过。
func ExpenseTravelFacts(p Project, item *ServiceItem, actor, occurredDate string, current *DeliveryEvent, sources map[string]DeliveryEvent) ExpenseTravelSnapshot {
	snapshot := unknownExpenseTravel("TRAVEL_PREPARATION_MISSING")
	if item == nil {
		return finishExpenseTravel(snapshot)
	}
	if current == nil {
		return finishExpenseTravel(snapshot)
	}
	snapshot.ReasonCodes = []string{}
	event := *current
	seen := map[string]bool{}
	for depth := 0; depth < MaxExpenseTravelSourceDepth; depth++ {
		if event.TenantID != p.TenantID || event.ProjectID != p.ID || event.ServiceItemID == "" || depth == 0 && event.ServiceItemID != item.ID {
			return failExpenseTravel(snapshot, "TRAVEL_SOURCE_SCOPE_MISMATCH")
		}
		if seen[event.ID] || event.ID == "" {
			return failExpenseTravel(snapshot, "TRAVEL_SOURCE_CYCLE_OR_INVALID_ID")
		}
		seen[event.ID] = true
		source := ExpenseTravelEventSource(event)
		if source.ContentDigest == "" {
			return failExpenseTravel(snapshot, "TRAVEL_SOURCE_CONTENT_INVALID")
		}
		snapshot.SourceChain = append(snapshot.SourceChain, source)
		if depth == 0 {
			snapshot.SourceEventID = event.ID
			snapshot.SourceEventVersion = source.EventVersion
		}
		if event.Type != "PREPARATION_STARTED" {
			return failExpenseTravel(snapshot, "TRAVEL_PREPARATION_REVOKED_OR_INVALID")
		}
		travel, ok := ExpenseTravelArrangement(event)
		if !ok {
			return failExpenseTravel(snapshot, "TRAVEL_STRUCTURED_FACTS_MISSING")
		}
		if depth == 0 {
			snapshot.Mode = travel.Mode
		}
		switch travel.Mode {
		case TravelModeNoTravel:
			if depth > 0 {
				return failExpenseTravel(snapshot, "TRAVEL_REFERENCE_HAS_NO_TRAVEL")
			}
			snapshot.Status = "KNOWN"
			participating := false
			snapshot.ActorInTravelers = &participating
			snapshot.ReasonCodes = append(snapshot.ReasonCodes, "NO_TRAVEL_IS_NOT_APPROVAL")
			return finishExpenseTravel(snapshot)
		case TravelModeNew:
			return knownExpenseTravel(snapshot, travel, item, actor, occurredDate)
		case TravelModeExisting:
			next, found := sources[travel.ReferenceEventID]
			if travel.ReferenceEventID == "" || !found {
				return failExpenseTravel(snapshot, "TRAVEL_REFERENCE_MISSING")
			}
			nextTravel, valid := ExpenseTravelArrangement(next)
			if !valid || travel.RequestID == "" || nextTravel.RequestID != travel.RequestID {
				return failExpenseTravel(snapshot, "TRAVEL_REFERENCE_REQUEST_MISMATCH")
			}
			event = next
		default:
			return failExpenseTravel(snapshot, "TRAVEL_MODE_UNKNOWN")
		}
	}
	return failExpenseTravel(snapshot, "TRAVEL_REFERENCE_DEPTH_LIMIT")
}

// knownExpenseTravel 验证来源行程基本字段并计算事实一致性，不制定费用政策阈值。
// 参数：snapshot为来源链，travel为NEW事实，item为现场，actor/occurredDate为当前主体及费用日。
// 返回值：已知事实或明确未知；城市映射无权威配置时始终UNKNOWN。
func knownExpenseTravel(snapshot ExpenseTravelSnapshot, travel TravelArrangementInput, item *ServiceItem, actor, occurredDate string) ExpenseTravelSnapshot {
	start, startErr := time.Parse("2006-01-02", travel.DepartureDate)
	end, endErr := time.Parse("2006-01-02", travel.ReturnDate)
	if startErr != nil || endErr != nil || end.Before(start) || strings.TrimSpace(travel.Origin) == "" || strings.TrimSpace(travel.Destination) == "" || len(travel.TravelerIDs) == 0 || travel.RequestID == "" {
		return failExpenseTravel(snapshot, "TRAVEL_FACTS_INCOMPLETE")
	}
	snapshot.Status = "KNOWN"
	snapshot.Origin = travel.Origin
	snapshot.Destination = travel.Destination
	snapshot.DepartureDate = travel.DepartureDate
	snapshot.ReturnDate = travel.ReturnDate
	snapshot.Transport = travel.Transport
	snapshot.AccommodationNeed = travel.AccommodationNeed
	participating := actor != "" && slices.Contains(travel.TravelerIDs, actor)
	snapshot.ActorInTravelers = &participating
	if item.Site != "" {
		snapshot.SiteMatchStatus = "MISMATCH"
		if strings.TrimSpace(item.Site) == strings.TrimSpace(travel.Destination) {
			snapshot.SiteMatchStatus = "MATCH"
		} else {
			snapshot.ReasonCodes = append(snapshot.ReasonCodes, "SITE_TEXT_MISMATCH")
		}
	}
	if date, err := time.Parse("2006-01-02", occurredDate); err == nil {
		snapshot.DateMatchStatus = "MATCH"
		if date.Before(start) || date.After(end) {
			snapshot.DateMatchStatus = "MISMATCH"
			snapshot.ReasonCodes = append(snapshot.ReasonCodes, "OCCURRED_DATE_OUTSIDE_TRAVEL_WINDOW")
		}
	}
	if !participating {
		snapshot.ReasonCodes = append(snapshot.ReasonCodes, "ACTOR_NOT_IN_TRAVELERS")
	}
	if strings.TrimSpace(travel.Transport) == "" {
		snapshot.ReasonCodes = append(snapshot.ReasonCodes, "TRAVEL_TRANSPORT_UNKNOWN")
	}
	if strings.TrimSpace(travel.AccommodationNeed) == "" {
		snapshot.ReasonCodes = append(snapshot.ReasonCodes, "TRAVEL_ACCOMMODATION_UNKNOWN")
	}
	snapshot.ReasonCodes = append(snapshot.ReasonCodes, "CITY_CODE_MAPPING_UNKNOWN", "PREPARATION_IS_NOT_TRAVEL_APPROVAL")
	return finishExpenseTravel(snapshot)
}

// failExpenseTravel 保留可追溯来源链但不输出不可信行程字段。
// 参数：snapshot为当前来源快照，reason为明确未知原因。
// 返回值：已完成摘要的UNKNOWN快照。
func failExpenseTravel(snapshot ExpenseTravelSnapshot, reason string) ExpenseTravelSnapshot {
	snapshot.Status = "UNKNOWN"
	snapshot.ReasonCodes = append(snapshot.ReasonCodes, reason)
	return finishExpenseTravel(snapshot)
}

// finishExpenseTravel 为完整最小事实生成内容摘要，包含来源链和当前主体参与布尔。
// 参数：snapshot为尚未计算自身摘要的快照。
// 返回值：不含原始参与人或敏感字段的内容版本快照。
func finishExpenseTravel(snapshot ExpenseTravelSnapshot) ExpenseTravelSnapshot {
	snapshot.ContentDigest = ""
	raw, _ := json.Marshal(snapshot)
	hash := sha256.Sum256(raw)
	snapshot.ContentDigest = hex.EncodeToString(hash[:])
	return snapshot
}

// AttachExpenseTravelSnapshot 把行程事实纳入费用引用版本，保持原资格和状态语义。
// 参数：reference为资格引用，snapshot为当前权威行程事实。
// 返回值：包含行程快照及变化敏感reference_version的引用。
func AttachExpenseTravelSnapshot(reference ExpenseProjectReference, snapshot ExpenseTravelSnapshot) ExpenseProjectReference {
	raw, _ := json.Marshal(struct {
		EligibilityVersion string
		Travel             ExpenseTravelSnapshot
	}{reference.ReferenceVersion, snapshot})
	hash := sha256.Sum256(raw)
	reference.ReferenceVersion = hex.EncodeToString(hash[:])
	reference.TravelSnapshot = snapshot
	return reference
}

// ExpenseTravelUnknown 暴露稳定未知快照供可信调用方表达缺事实或缺范围，不伪造默认行程。
// 参数：reason为稳定业务原因码，不接受浏览器传入的授权结论。
// 返回值：具有完整内容摘要的UNKNOWN事实。
func ExpenseTravelUnknown(reason string) ExpenseTravelSnapshot {
	return finishExpenseTravel(unknownExpenseTravel(reason))
}
