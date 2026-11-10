package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// expenseTravelEventFixture 构造包含稳定主体及准备事实的不可变事件。
// 参数：id/item/mode为来源标识及模式，reference为已有行程引用。
// 返回值：同租户同项目的合成事件，不代表真实出差审批。
func expenseTravelEventFixture(id, item, mode, reference string) DeliveryEvent {
	return DeliveryEvent{ID: id, TenantID: "t", ProjectID: "p", ServiceItemID: item, Type: "PREPARATION_STARTED", CreatedAt: time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC), Payload: map[string]any{"expected_version": uint64(3), "travel": TravelArrangementInput{Mode: mode, RequestID: "trip", ReferenceEventID: reference, Origin: "出发地", Destination: "目的地", DepartureDate: "2026-11-09", ReturnDate: "2026-11-11", Transport: "铁路", AccommodationNeed: "需要", TravelerIDs: []string{"u", "private-person"}}}}
}

// TestExpenseTravelFactsAndReferenceVersion 验证已有来源精确追溯且只输出当前主体参与布尔。
// 参数：t为测试上下文。
// 返回值：无；来源、隐私或变动版本丢失时失败。
func TestExpenseTravelFactsAndReferenceVersion(t *testing.T) {
	p := Project{ID: "p", TenantID: "t", OwnerIdentityID: "u", SourceKind: "NATIVE", Version: 1}
	item := ServiceItem{ID: "a", ProjectID: "p", TenantID: "t", Site: "目的地", Version: 2}
	source := expenseTravelEventFixture("source", "b", "NEW", "")
	current := expenseTravelEventFixture("current", "a", "EXISTING", "source")
	snapshot := ExpenseTravelFacts(p, &item, "u", "2026-11-10", &current, map[string]DeliveryEvent{"source": source})
	if snapshot.Status != "KNOWN" || snapshot.Mode != "EXISTING" || len(snapshot.SourceChain) != 2 || snapshot.ActorInTravelers == nil || !*snapshot.ActorInTravelers || snapshot.DepartureDate != "2026-11-09" || snapshot.SiteMatchStatus != "MATCH" || snapshot.DateMatchStatus != "MATCH" || snapshot.CityCodeStatus != "UNKNOWN" || !strings.HasPrefix(snapshot.SourceEventVersion, "sha256:") {
		t.Fatalf("snapshot %+v", snapshot)
	}
	nonParticipant := ExpenseTravelFacts(p, &item, "not-member", "2026-11-10", &current, map[string]DeliveryEvent{"source": source})
	if nonParticipant.ActorInTravelers == nil || *nonParticipant.ActorInTravelers {
		t.Fatal("travel actor falsely included")
	}
	data, _ := json.Marshal(snapshot)
	if strings.Contains(string(data), "private-person") || strings.Contains(string(data), "traveler_ids") {
		t.Fatal("other participant leaked")
	}
	ref := ExpenseReference(p, &item, "u", nil, time.Now().UTC())
	first := AttachExpenseTravelSnapshot(ref, snapshot)
	source.Payload["travel"] = TravelArrangementInput{Mode: "NEW", RequestID: "trip", Origin: "出发地", Destination: "异地", DepartureDate: "2026-11-09", ReturnDate: "2026-11-11", TravelerIDs: []string{"u"}}
	changed := ExpenseTravelFacts(p, &item, "u", "2026-11-10", &current, map[string]DeliveryEvent{"source": source})
	second := AttachExpenseTravelSnapshot(ref, changed)
	if first.ReferenceVersion == second.ReferenceVersion || snapshot.ContentDigest == changed.ContentDigest || changed.SiteMatchStatus != "MISMATCH" {
		t.Fatal("travel change omitted from reference version")
	}
}

// TestExpenseTravelUnknownNoTravelAndInvalidChains 验证计划不冒充行程且错误租户项目循环不猜来源。
// 参数：t为测试上下文。
// 返回值：无；无事实被当已知或非法链被接受时失败。
func TestExpenseTravelUnknownNoTravelAndInvalidChains(t *testing.T) {
	p := Project{ID: "p", TenantID: "t"}
	item := ServiceItem{ID: "a", ProjectID: "p", TenantID: "t", PlannedStart: "2026-11-09", PlannedEnd: "2026-11-11"}
	if snapshot := ExpenseTravelFacts(p, &item, "u", "2026-11-10", nil, nil); snapshot.Status != "UNKNOWN" || snapshot.DepartureDate != "" {
		t.Fatal("service plan became travel", snapshot)
	}
	noTravel := expenseTravelEventFixture("none", "a", "NO_TRAVEL", "")
	snapshot := ExpenseTravelFacts(p, &item, "u", "2026-11-10", &noTravel, nil)
	if snapshot.Status != "KNOWN" || snapshot.Mode != "NO_TRAVEL" || snapshot.SourceEventID != "none" {
		t.Fatal(snapshot)
	}
	current := expenseTravelEventFixture("current", "a", "EXISTING", "source")
	source := expenseTravelEventFixture("source", "b", "NEW", "")
	for _, foreign := range []string{"tenant", "project"} {
		wrong := source
		if foreign == "tenant" {
			wrong.TenantID = "foreign"
		} else {
			wrong.ProjectID = "foreign"
		}
		if value := ExpenseTravelFacts(p, &item, "u", "2026-11-10", &current, map[string]DeliveryEvent{"source": wrong}); value.Status != "UNKNOWN" || value.Destination != "" {
			t.Fatal("foreign source accepted", foreign)
		}
	}
	if value := ExpenseTravelFacts(p, &item, "u", "2026-11-10", &current, nil); value.Status != "UNKNOWN" {
		t.Fatal("missing source guessed")
	}
	cyclic := expenseTravelEventFixture("source", "b", "EXISTING", "current")
	if value := ExpenseTravelFacts(p, &item, "u", "2026-11-10", &current, map[string]DeliveryEvent{"source": cyclic, "current": current}); value.Status != "UNKNOWN" {
		t.Fatal("cycle accepted")
	}
	revoked := current
	revoked.Type = "PREPARATION_REVOKED"
	if value := ExpenseTravelFacts(p, &item, "u", "2026-11-10", &revoked, nil); value.Status != "UNKNOWN" {
		t.Fatal("revoked preparation accepted")
	}
}
