package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Stored status/count/progress must never override the complete active item set.
func TestProjectProjectionMatchesAuthoritativeDomain(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 123, time.FixedZone("CST", 8*3600))
	archived := now.Add(time.Hour)
	for _, tc := range []struct {
		name, supplement, stored string
		items                    []ProjectSnapshotItem
		status                   string
		progress, count          int
		risk, flight             bool
	}{
		{"completed with termination", "", "实施中", []ProjectSnapshotItem{{Status: ProjectStatusFieldCompleted, ReportStatus: "ARCHIVED"}, {Status: ProjectStatusTerminated}}, ProjectStatusCompleted, 100, 2, true, false},
		{"archive excluded", "", "实施中", []ProjectSnapshotItem{{Status: ProjectStatusFieldCompleted, ReportStatus: "ARCHIVED"}, {Status: ProjectStatusException, ArchivedAt: &archived}}, ProjectStatusCompleted, 100, 1, false, false},
		{"supplement takes priority", "REQUIRED", "", []ProjectSnapshotItem{{Status: ProjectStatusFieldCompleted, ReportStatus: "ARCHIVED"}}, ProjectStatusSupplementRequired, 100, 1, false, true},
		{"empty project", "", "待实施", nil, ProjectStatusPendingExecution, 0, 0, false, true},
		{"unknown is conservative", "", "已完成", []ProjectSnapshotItem{{Status: "UNKNOWN"}}, ProjectStatusPendingDecomposition, 0, 1, false, true},
		{"terminated", "", "", []ProjectSnapshotItem{{Status: ProjectStatusTerminated}}, ProjectStatusTerminated, 0, 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjectProjection(Project{TenantID: "tenant", ID: "P-1", Name: "中文项目", Customer: "客户", CustomerID: "C-1", Contract: "合同", ContractID: "HT-1", Category: "类别", Team: "展示团队", Manager: "经理", ManagerIdentityID: "manager-id", OwnerOrgID: "org-id", OwnerIdentityID: "owner-id", Services: 999, Progress: 98, Status: tc.stored, SupplementStatus: tc.supplement, CreatedAt: now, UpdatedAt: now}, "snapshot", tc.items, now)
			if p.Type != "project" || p.Status != tc.status || p.Progress != tc.progress || p.ServiceItems != tc.count || p.Risk != tc.risk || p.InFlight != tc.flight {
				t.Fatalf("projection=%+v", p)
			}
			for key, pair := range map[string][2]string{"customer": {p.Customer, "客户"}, "customer_id": {p.CustomerID, "C-1"}, "contract": {p.Contract, "合同"}, "contract_id": {p.ContractID, "HT-1"}, "category": {p.Category, "类别"}, "team": {p.Team, "展示团队"}, "manager": {p.Manager, "经理"}, "manager_identity_id": {p.ManagerIdentityID, "manager-id"}, "owner_org_id": {p.OwnerOrgID, "org-id"}, "owner_identity_id": {p.OwnerIdentityID, "owner-id"}} {
				if pair[0] != pair[1] {
					t.Fatalf("%s=%q want %q", key, pair[0], pair[1])
				}
			}
			line, err := ProjectSnapshotLine(p)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err = json.Unmarshal(line, &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 23 || fields["created_at"] != "2026-10-23T02:00:00.000000123Z" || !strings.Contains(string(line), "中文项目") {
				t.Fatalf("wire fields=%s", line)
			}
		})
	}
}

func TestProjectSnapshotLineRejectsInvalidUTF8(t *testing.T) {
	_, err := ProjectSnapshotLine(ProjectProjection{Type: "project", Name: string([]byte{0xff})})
	if err == nil {
		t.Fatal("invalid UTF-8 must not silently become replacement characters")
	}
}
