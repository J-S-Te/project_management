package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/platform"
)

// fakeApprovedContractDirectory 模拟合同侧 approved-contract-references：
// id 升序游标、单页上限、keyword 只匹配合同编号/标题/客户名，且只返回审批通过的合同。
type fakeApprovedContractDirectory struct {
	items    []platform.ApprovedContract
	calls    []fakeDirectoryCall
	failWith error
}

type fakeDirectoryCall struct {
	keyword, afterID string
	limit            int
}

func (d *fakeApprovedContractDirectory) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return append([]platform.ApprovedContract(nil), d.items...), nil
}

func (d *fakeApprovedContractDirectory) ListReferences(_ context.Context, keyword, afterID string, limit int) ([]platform.ApprovedContract, string, error) {
	d.calls = append(d.calls, fakeDirectoryCall{keyword: keyword, afterID: afterID, limit: limit})
	if d.failWith != nil {
		return nil, "", d.failWith
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	keyword = strings.TrimSpace(keyword)
	result := make([]platform.ApprovedContract, 0, limit)
	lastID := ""
	for _, item := range d.items {
		if afterID != "" && item.ID <= afterID {
			continue
		}
		if keyword != "" && !strings.Contains(item.Number, keyword) && !strings.Contains(item.Title, keyword) && !strings.Contains(item.CustomerName, keyword) {
			continue
		}
		result = append(result, item)
		lastID = item.ID
		if len(result) >= limit {
			break
		}
	}
	next := ""
	if len(result) == limit {
		next = lastID
	}
	return result, next, nil
}

func (d *fakeApprovedContractDirectory) Get(_ context.Context, id string) (platform.ApprovedContract, error) {
	for _, item := range d.items {
		if item.ID == id {
			return item, nil
		}
	}
	return platform.ApprovedContract{}, errors.New("not found")
}

func (d *fakeApprovedContractDirectory) GetServiceItems(context.Context, string) (platform.ApprovedContractServiceCatalog, error) {
	return platform.ApprovedContractServiceCatalog{}, nil
}

// approvedContractDirectoryFixture 构造 250 份普通已审批合同：超过单页 200 上限，
// 用来验证选择器不再被静默截断。
func approvedContractDirectoryFixture(total int) []platform.ApprovedContract {
	items := make([]platform.ApprovedContract, 0, total)
	for index := 1; index <= total; index++ {
		items = append(items, platform.ApprovedContract{
			ID: fmt.Sprintf("C-%04d", index), Number: fmt.Sprintf("HT-%04d", index),
			Title: fmt.Sprintf("测评服务 %03d", index), CustomerName: fmt.Sprintf("客户 %03d", index),
			Version: 1, Status: "active", ApprovalPassed: true, OriginKind: "NORMAL", ImportMode: "NORMAL",
		})
	}
	return items
}

func createProjectPrincipal(roles ...string) platform.Principal {
	return platform.Principal{
		TenantID: "tenant-1", IdentityID: "identity-1", UserID: "identity-1", Roles: roles,
		Permissions: map[string]bool{"project.create": true},
		DataScopes:  []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}},
	}
}

// collectSearchPages 模拟前端"加载更多"：反复用上一页游标请求，直到上游耗尽。
func collectSearchPages(t *testing.T, service *Service, principal platform.Principal, keyword string, limit int) []platform.ApprovedContract {
	t.Helper()
	collected := make([]platform.ApprovedContract, 0, 300)
	afterID := ""
	for page := 0; page < 50; page++ {
		items, nextAfterID, err := service.SearchAvailableApprovedContracts(context.Background(), principal, keyword, afterID, limit)
		if err != nil {
			t.Fatal(err)
		}
		collected = append(collected, items...)
		if nextAfterID == "" || nextAfterID == afterID {
			return collected
		}
		afterID = nextAfterID
	}
	t.Fatal("游标翻页没有终止")
	return nil
}

// TestSearchAvailableApprovedContractsPagesPastTwoHundred 固定 BIZ-XAPI-09 的核心：
// 超过 200 份合同时必须能通过游标分页把所有可选项都取到（含最早与最新），
// 且不重复、不丢项 —— 而不是静默截断成前 200 条。
func TestSearchAvailableApprovedContractsPagesPastTwoHundred(t *testing.T) {
	directory := &fakeApprovedContractDirectory{items: approvedContractDirectoryFixture(250)}
	service := &Service{Repo: &scopeRepository{}, Contracts: directory}
	principal := createProjectPrincipal("admin")

	collected := collectSearchPages(t, service, principal, "", 100)
	if len(collected) != 250 {
		t.Fatalf("收集到 %d 份合同，want 250", len(collected))
	}
	seen := map[string]bool{}
	for index, item := range collected {
		if seen[item.ID] {
			t.Fatalf("合同 %s 重复出现", item.ID)
		}
		seen[item.ID] = true
		if want := fmt.Sprintf("C-%04d", index+1); item.ID != want {
			t.Fatalf("第 %d 条=%s，want %s（必须保持 id 升序）", index+1, item.ID, want)
		}
	}
	if !seen["C-0001"] || !seen["C-0250"] {
		t.Fatalf("最早或最新合同不可达: first=%v last=%v", seen["C-0001"], seen["C-0250"])
	}
	// 第一页不能把 200 条上游窗口直接当成可选项全部塞给前端。
	if len(directory.calls) < 2 {
		t.Fatalf("250 份合同必须跨页读取，实际调用 %d 次", len(directory.calls))
	}
	if directory.calls[0].limit > 200 {
		t.Fatalf("上游单页 limit=%d 超过合同侧上限 200", directory.calls[0].limit)
	}
}

// TestSearchAvailableApprovedContractsFindsContractBeyondFirstWindow 固定"搜索选中"：
// 关键字命中第 250 条（首屏 200 条窗口之外）时，必须能返回它。
func TestSearchAvailableApprovedContractsFindsContractBeyondFirstWindow(t *testing.T) {
	directory := &fakeApprovedContractDirectory{items: approvedContractDirectoryFixture(250)}
	service := &Service{Repo: &scopeRepository{}, Contracts: directory}
	principal := createProjectPrincipal("admin")

	items, nextAfterID, err := service.SearchAvailableApprovedContracts(context.Background(), principal, " HT-0250 ", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "C-0250" || nextAfterID != "" {
		t.Fatalf("items=%+v next=%q", items, nextAfterID)
	}
	// 关键字必须去空白后透传给合同侧，不能在前端做本地过滤。
	if len(directory.calls) != 1 || directory.calls[0].keyword != "HT-0250" {
		t.Fatalf("关键字未透传: %+v", directory.calls)
	}
}

// TestSearchAvailableApprovedContractsAppliesPostFilters 固定后置过滤：
// 审批未通过、非普通来源、已建项的合同都不能出现在可选项里，即使它们落在同页窗口内。
func TestSearchAvailableApprovedContractsAppliesPostFilters(t *testing.T) {
	fixture := approvedContractDirectoryFixture(10)
	fixture = append(fixture,
		platform.ApprovedContract{ID: "C-0101", Number: "HT-0101", ApprovalPassed: false, Status: "draft", OriginKind: "NORMAL", ImportMode: "NORMAL"},
		platform.ApprovedContract{ID: "C-0102", Number: "HT-0102", ApprovalPassed: true, Status: "active", OriginKind: "IMPORT", ImportMode: "NORMAL"},
		platform.ApprovedContract{ID: "C-0103", Number: "HT-0103", ApprovalPassed: true, Status: "active", OriginKind: "NORMAL", ImportMode: "LEGACY"},
		platform.ApprovedContract{ID: "", Number: "HT-0104", ApprovalPassed: true, Status: "active", OriginKind: "NORMAL", ImportMode: "NORMAL"},
	)
	repository := &scopeRepository{existingContractIDs: map[string]bool{"C-0002": true, "C-0007": true}}
	service := &Service{Repo: repository, Contracts: &fakeApprovedContractDirectory{items: fixture}}
	principal := createProjectPrincipal("admin")

	items, _, err := service.SearchAvailableApprovedContracts(context.Background(), principal, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}
	want := []string{"C-0001", "C-0003", "C-0004", "C-0005", "C-0006", "C-0008", "C-0009", "C-0010"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("可用合同=%v，want %v", got, want)
	}
}

// TestSearchAvailableApprovedContractsStopsAtCursorPageHardCap 固定游标循环硬上限：
// 上游游标一直前进但不结束时，一次请求最多拉 20 页，不会变成无界循环。
func TestSearchAvailableApprovedContractsStopsAtCursorPageHardCap(t *testing.T) {
	calls := 0
	verifier := &stubReferenceVerifier{references: func(_ string, afterID string, limit int) ([]platform.ApprovedContract, string, error) {
		calls++
		next := fmt.Sprintf("C-%04d", calls)
		// 每页都返回一条可用合同且游标持续前进 —— 只有硬上限能终止它。
		return []platform.ApprovedContract{{
			ID: next, Number: "HT-" + next, ApprovalPassed: true, Status: "active",
			OriginKind: "NORMAL", ImportMode: "NORMAL", Version: 1,
		}}, next, nil
	}}
	service := &Service{Repo: &scopeRepository{}, Contracts: verifier}
	principal := createProjectPrincipal("admin")

	items, nextAfterID, err := service.SearchAvailableApprovedContracts(context.Background(), principal, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if calls != approvedContractSearchMaxPages {
		t.Fatalf("上游调用 %d 次，want 硬上限 %d 次", calls, approvedContractSearchMaxPages)
	}
	if len(items) != approvedContractSearchMaxPages {
		t.Fatalf("收集 %d 条，want %d", len(items), approvedContractSearchMaxPages)
	}
	// 硬上限不是静默截断：必须给出可继续的游标，让前端能显式"加载更多"。
	if nextAfterID == "" {
		t.Fatal("硬上限中止必须返回可继续的游标")
	}
}

// TestSearchAvailableApprovedContractsRejectsNonCreators 固定权限：
// 没有 project.create（或非 admin/business_admin）一律 403，且不触达合同依赖。
func TestSearchAvailableApprovedContractsRejectsNonCreators(t *testing.T) {
	directory := &fakeApprovedContractDirectory{items: approvedContractDirectoryFixture(3)}
	service := &Service{Repo: &scopeRepository{}, Contracts: directory}
	for _, principal := range []platform.Principal{
		{TenantID: "tenant-1", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}},
		createProjectPrincipal("engineer"),
	} {
		if _, _, err := service.SearchAvailableApprovedContracts(context.Background(), principal, "", "", 50); !errors.Is(err, ErrForbidden) {
			t.Fatalf("principal roles=%v error=%v，want ErrForbidden", principal.Roles, err)
		}
	}
	if len(directory.calls) != 0 {
		t.Fatalf("越权请求不应触达合同依赖: %+v", directory.calls)
	}
}

// TestSearchAvailableApprovedContractsKeepsDependencyUnavailable 固定 fail-closed：
// 合同依赖失败必须保持 ErrContractUnavailable（503），不能降级成空列表。
func TestSearchAvailableApprovedContractsKeepsDependencyUnavailable(t *testing.T) {
	service := &Service{Repo: &scopeRepository{}, Contracts: &fakeApprovedContractDirectory{failWith: errors.New("contract service down")}}
	if _, _, err := service.SearchAvailableApprovedContracts(context.Background(), createProjectPrincipal("admin"), "", "", 50); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("error=%v，want ErrContractUnavailable", err)
	}
	unconfigured := &Service{Repo: &scopeRepository{}}
	if _, _, err := unconfigured.SearchAvailableApprovedContracts(context.Background(), createProjectPrincipal("admin"), "", "", 50); !errors.Is(err, ErrContractUnavailable) {
		t.Fatalf("未接线合同依赖 error=%v，want ErrContractUnavailable", err)
	}
}

// stubReferenceVerifier 用闭包描述 ListReferences 行为，其余方法保持空实现。
type stubReferenceVerifier struct {
	references func(keyword, afterID string, limit int) ([]platform.ApprovedContract, string, error)
}

func (v *stubReferenceVerifier) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return nil, nil
}

func (v *stubReferenceVerifier) ListReferences(_ context.Context, keyword, afterID string, limit int) ([]platform.ApprovedContract, string, error) {
	return v.references(keyword, afterID, limit)
}

func (v *stubReferenceVerifier) Get(context.Context, string) (platform.ApprovedContract, error) {
	return platform.ApprovedContract{}, nil
}

func (v *stubReferenceVerifier) GetServiceItems(context.Context, string) (platform.ApprovedContractServiceCatalog, error) {
	return platform.ApprovedContractServiceCatalog{}, nil
}
