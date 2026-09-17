package application

import (
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

func TestBuildMonitoringProjectAggregatesMilestoneResourcesAndServiceStates(t *testing.T) {
	row := buildMonitoringProject(domain.Project{ID: "PJ-1", Status: "实施中"}, []domain.ServiceItem{
		{ID: "SI-1", ProjectID: "PJ-1", Status: "实施中", Category: "渗透测试", ProjectManagerID: "pm-1", EngineerIDs: []string{"e-1"}, ConflictStatus: "CONFLICT", ImplementationPlan: &domain.ImplementationPlan{Equipment: []domain.PlanResource{{ResourceID: "eq-1", ValidUntil: "2020-01-01"}}}},
		{ID: "SI-2", ProjectID: "PJ-1", Status: "待实施", Category: "等保测评", ProjectManagerID: "pm-1", EngineerIDs: []string{"e-1", "e-2"}, ImplementationPlan: &domain.ImplementationPlan{Equipment: []domain.PlanResource{{ResourceID: "eq-2", ReturnedAt: "2026-09-17T00:00:00Z"}}}},
	}, []domain.SlaOverdueItem{{ID: "SI-1", ProjectID: "PJ-1", Kind: domain.SlaKindStatusOverdue}}, map[string]domain.DeliveryEvent{
		"PJ-1": {ID: "EV-1", ProjectID: "PJ-1", Type: EventFieldRecordSubmitted, CreatedAt: time.Now().UTC()},
	})
	if row.Project.Services != 2 || row.ServiceStatusCounts["实施中"] != 1 || row.ServiceStatusCounts["待实施"] != 1 {
		t.Fatalf("service summary = %+v, project=%+v", row.ServiceStatusCounts, row.Project)
	}
	if row.Milestone.Current != "现场实施" || row.Milestone.Next != "报告编制" || row.ActionSection != "implementation" {
		t.Fatalf("milestone/action = %+v / %s", row.Milestone, row.ActionSection)
	}
	if row.Resources.Engineers != 2 || row.Resources.Equipment != 2 || row.Resources.ConflictItems != 1 || row.Resources.ExpiredEquipment != 1 || row.Resources.ReleasedEquipment != 1 {
		t.Fatalf("resources = %+v", row.Resources)
	}
	if len(row.SLAItems) != 1 || row.LatestEvent == nil || row.LatestEvent.ID != "EV-1" {
		t.Fatalf("sla/event = %+v / %+v", row.SLAItems, row.LatestEvent)
	}
}

func TestMonitoringMilestoneHandlesExceptionAndTermination(t *testing.T) {
	if got := monitoringMilestone("异常处理中"); got.Current != "异常处理" || got.Next != "恢复原流程" {
		t.Fatalf("exception milestone = %+v", got)
	}
	if got := monitoringMilestone("已终止"); got.Current != "项目终止" || got.Next != "" {
		t.Fatalf("terminated milestone = %+v", got)
	}
}
