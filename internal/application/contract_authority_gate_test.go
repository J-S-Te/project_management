package application

// BIZ-PROJECT-01 / BIZ-PROJECT-02 的行为基线：
//   - 用户会话建项入口不得信任请求体中的合同号/客户/客户 ID/服务项，必须按
//     contract_id 回读合同服务并比对版本、按服务项目录重建范围；
//   - 机器入口（合同生效投递）保留可信投递语义，与用户入口分开；
//   - 拆解调整必须按补充协议服务项目录重建服务项，目录外/重复/目录不可用一律拒绝。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// authorityContractVerifier 是可编程的合同服务桩：合同与目录都能独立注入错误。
type authorityContractVerifier struct {
	contract   platform.ApprovedContract
	catalog    platform.ApprovedContractServiceCatalog
	getErr     error
	catalogErr error
	getCalls   int
}

func (v *authorityContractVerifier) List(context.Context, int) ([]platform.ApprovedContract, error) {
	return nil, v.getErr
}

func (v *authorityContractVerifier) ListReferences(context.Context, string, string, int) ([]platform.ApprovedContract, string, error) {
	return nil, "", v.getErr
}

func (v *authorityContractVerifier) Get(context.Context, string) (platform.ApprovedContract, error) {
	v.getCalls++
	if v.getErr != nil {
		return platform.ApprovedContract{}, v.getErr
	}
	return v.contract, nil
}

func (v *authorityContractVerifier) GetServiceItems(context.Context, string) (platform.ApprovedContractServiceCatalog, error) {
	if v.catalogErr != nil {
		return platform.ApprovedContractServiceCatalog{}, v.catalogErr
	}
	return v.catalog, nil
}

// capturingActivationRepository 记录激活落库的项目与服务项，便于断言权威字段。
type capturingActivationRepository struct {
	duplicateContractRepository
	project domain.Project
	items   []domain.ServiceItem
}

func (r *capturingActivationRepository) ActivateContract(_ context.Context, project domain.Project, items []domain.ServiceItem, _ domain.DeliveryEvent) error {
	r.project = project
	r.items = items
	return nil
}

func authoritativeContract() platform.ApprovedContract {
	return platform.ApprovedContract{
		ID: "C-1", Number: "HT-AUTH", Title: "权威合同", CustomerID: "CUST-AUTH", CustomerName: "权威客户",
		Version: 7, Status: "active", ApprovalPassed: true,
	}
}

func authoritativeCatalog() platform.ApprovedContractServiceCatalog {
	return platform.ApprovedContractServiceCatalog{
		ContractID: "C-1", ContractVersion: 7,
		ServiceItems: []platform.ApprovedContractService{
			{SourceID: "SVC-1", Name: "等保测评", ServiceType: "等保测评", Site: "合同场所", Batch: "B-AUTH", Category: "等保测评", System: "权威系统", SystemLevel: "三级", Requirement: "权威要求", TestMode: "STANDARD"},
		},
	}
}

func userActivationPrincipal() platform.Principal {
	return principalWith("project.contract.import", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})
}

func userActivationInput() domain.ContractActivation {
	return domain.ContractActivation{
		ContractID: "C-1",
		// 以下字段全部是伪造值：用户入口必须丢弃它们。
		ContractNumber: "HT-FORGED", ContractVersion: "7", ContractName: "伪造合同名称",
		Customer: "伪造客户", CustomerID: "FORGED-CUSTOMER-ID",
		EffectiveAt: time.Now().UTC().Truncate(time.Second),
		Services: []domain.ContractService{{
			SourceID: "SVC-1", Site: "客户现场", SiteCode: "",
			Batch: "伪造批次", Category: "伪造类别", System: "伪造系统", SystemLevel: "伪造级别",
			Requirement: "伪造要求", TestMode: "PENETRATION",
		}},
	}
}

// 只有合同号、没有稳定 contract_id 的用户请求必须拒绝，且不得触达合同服务。
func TestUserActivationRejectsNumberOnlyPayload(t *testing.T) {
	verifier := &authorityContractVerifier{contract: authoritativeContract(), catalog: authoritativeCatalog()}
	service := &Service{Repo: &capturingActivationRepository{}, Contracts: verifier}
	input := userActivationInput()
	input.ContractID = ""
	input.ContractNumber = "HT-AUTH"
	_, err := service.ActivateContractFromUser(context.Background(), userActivationPrincipal(), input)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err=%v, want ErrValidation", err)
	}
	if verifier.getCalls != 0 {
		t.Fatalf("number-only payload reached contract service %d times", verifier.getCalls)
	}
}

// 用户入口成功后，客户/客户 ID/合同号/版本与服务项归类必须来自合同权威读取。
func TestUserActivationRebuildsContractAndServicesFromAuthority(t *testing.T) {
	repository := &capturingActivationRepository{}
	service := &Service{Repo: repository, Contracts: &authorityContractVerifier{contract: authoritativeContract(), catalog: authoritativeCatalog()}}
	project, err := service.ActivateContractFromUser(context.Background(), userActivationPrincipal(), userActivationInput())
	if err != nil {
		t.Fatalf("activation failed: %v", err)
	}
	if project.ContractID != "C-1" || project.Contract != "HT-AUTH" || project.ContractVersion != "7" {
		t.Fatalf("contract identity not authoritative: %+v", project)
	}
	if project.Customer != "权威客户" || project.CustomerID != "CUST-AUTH" {
		t.Fatalf("customer not authoritative: customer=%q customer_id=%q", project.Customer, project.CustomerID)
	}
	if project.Name != "权威合同" {
		t.Fatalf("project name=%q, want contract title", project.Name)
	}
	if len(repository.items) != 1 {
		t.Fatalf("items=%+v", repository.items)
	}
	item := repository.items[0]
	if item.SourceServiceID != "SVC-1" || item.Site != "客户现场" {
		t.Fatalf("selection identity/site lost: %+v", item)
	}
	if item.Batch != "B-AUTH" || item.Category != "等保测评" || item.System != "权威系统" {
		t.Fatalf("service classification not rebuilt from catalog: %+v", item)
	}
	if item.TestMode != "STANDARD" {
		t.Fatalf("client-forged test mode leaked: %q", item.TestMode)
	}
}

// 客户端带上过期合同版本时必须拒绝，不能静默按最新版本落库。
func TestUserActivationRejectsStaleContractVersion(t *testing.T) {
	repository := &capturingActivationRepository{}
	service := &Service{Repo: repository, Contracts: &authorityContractVerifier{contract: authoritativeContract(), catalog: authoritativeCatalog()}}
	input := userActivationInput()
	input.ContractVersion = "6"
	_, err := service.ActivateContractFromUser(context.Background(), userActivationPrincipal(), input)
	if !errors.Is(err, ErrPrecondition) {
		t.Fatalf("err=%v, want ErrPrecondition", err)
	}
	if repository.project.ID != "" {
		t.Fatal("stale version still created a project")
	}
}

// 合同不存在/未审批/非普通来源一律失败关闭，且不落库。
func TestUserActivationFailsClosedWithoutUsableAuthority(t *testing.T) {
	unapproved := authoritativeContract()
	unapproved.ApprovalPassed = false
	pending := authoritativeContract()
	pending.Status = "pending"
	imported := authoritativeContract()
	imported.OriginKind, imported.ImportMode, imported.OriginImportItemID = "IMPORT", "IMPORT", "ITEM-1"
	cases := []struct {
		name     string
		contract platform.ApprovedContract
		getErr   error
		want     error
	}{
		{"missing contract", platform.ApprovedContract{}, errors.New("404"), ErrContractUnavailable},
		{"approval flag missing", unapproved, nil, ErrPrecondition},
		{"status not approved", pending, nil, ErrPrecondition},
		{"import origin", imported, nil, ErrPrecondition},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &capturingActivationRepository{}
			service := &Service{Repo: repository, Contracts: &authorityContractVerifier{contract: testCase.contract, catalog: authoritativeCatalog(), getErr: testCase.getErr}}
			_, err := service.ActivateContractFromUser(context.Background(), userActivationPrincipal(), userActivationInput())
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err=%v, want %v", err, testCase.want)
			}
			if repository.project.ID != "" {
				t.Fatal("unusable authority still created a project")
			}
		})
	}
}

// 合同服务项目录不可用或与合同版本不一致时必须失败关闭，不得回退到请求体清单。
func TestUserActivationFailsClosedWhenServiceCatalogUnavailable(t *testing.T) {
	staleCatalog := authoritativeCatalog()
	staleCatalog.ContractVersion = 8
	cases := []struct {
		name       string
		verifier   *authorityContractVerifier
		want       error
		wantDetail string
	}{
		{"catalog dependency failure", &authorityContractVerifier{contract: authoritativeContract(), catalogErr: errors.New("catalog down")}, ErrContractUnavailable, "catalog down"},
		{"catalog version drift", &authorityContractVerifier{contract: authoritativeContract(), catalog: staleCatalog}, ErrPrecondition, "服务范围"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &capturingActivationRepository{}
			service := &Service{Repo: repository, Contracts: testCase.verifier}
			_, err := service.ActivateContractFromUser(context.Background(), userActivationPrincipal(), userActivationInput())
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err=%v, want %v", err, testCase.want)
			}
			if repository.project.ID != "" {
				t.Fatal("unavailable catalog still created a project")
			}
		})
	}
}

// 所选 source_id 必须存在于合同服务项目录；目录外选择与重复选择都拒绝。
func TestUserActivationRejectsSourcesOutsideCatalog(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		services []domain.ContractService
	}{
		{"outside catalog", []domain.ContractService{{SourceID: "SVC-UNKNOWN", Site: "客户现场"}}},
		{"duplicate selection", []domain.ContractService{{SourceID: "SVC-1", Site: "客户现场"}, {SourceID: "SVC-1", Site: "另一现场"}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &capturingActivationRepository{}
			service := &Service{Repo: repository, Contracts: &authorityContractVerifier{contract: authoritativeContract(), catalog: authoritativeCatalog()}}
			input := userActivationInput()
			input.Services = testCase.services
			_, err := service.ActivateContractFromUser(context.Background(), userActivationPrincipal(), input)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("err=%v, want ErrValidation", err)
			}
			if repository.project.ID != "" {
				t.Fatal("invalid selection still created a project")
			}
		})
	}
}

// 机器入口（合同生效投递）保留可信投递语义：不回读合同服务，按载荷落库。
func TestMachineActivationKeepsTrustedDeliverySemantics(t *testing.T) {
	repository := &capturingActivationRepository{}
	// 合同服务桩一被调用就报错：机器入口不应需要它。
	service := &Service{Repo: repository, Contracts: &authorityContractVerifier{getErr: errors.New("must not be called"), catalogErr: errors.New("must not be called")}}
	principal := principalWith("project.contract.import", platform.DataScope{RoleCode: "system_integration", ScopeType: "APPLICATION"})
	input := domain.ContractActivation{
		ContractID: "C-MACHINE", ContractNumber: "HT-MACHINE", ContractVersion: "3",
		Customer: "合同客户", CustomerID: "CUST-M", EffectiveAt: time.Now().UTC(),
		Services: []domain.ContractService{{SourceID: "S-M", Site: "现场", Batch: "B1", Category: "等保测评", TestMode: "STANDARD"}},
	}
	project, err := service.ActivateContract(context.Background(), principal, input)
	if err != nil {
		t.Fatalf("machine delivery rejected: %v", err)
	}
	if project.Contract != "HT-MACHINE" || project.Customer != "合同客户" || project.ContractVersion != "3" {
		t.Fatalf("machine payload not persisted as delivered: %+v", project)
	}
}

// ---- BIZ-PROJECT-02：拆解调整必须校验补充协议服务项目录 ----

func adjustmentPrincipal() platform.Principal {
	return principalWith("project.decomposition.manage", platform.DataScope{RoleCode: "business_admin", ScopeType: "APPLICATION"})
}

func supplementVerifier(catalog platform.ApprovedContractServiceCatalog, catalogErr error) *authorityContractVerifier {
	return &authorityContractVerifier{
		contract: platform.ApprovedContract{ID: "SUP-1", Number: "HT-SUP", CustomerID: "customer", CustomerName: "示例客户", Version: 3, Status: "active", ApprovalPassed: true},
		catalog:  catalog, catalogErr: catalogErr,
	}
}

func supplementCatalog() platform.ApprovedContractServiceCatalog {
	return platform.ApprovedContractServiceCatalog{
		ContractID: "SUP-1", ContractVersion: 3,
		ServiceItems: []platform.ApprovedContractService{
			{SourceID: "SVC-1", Name: "等保测评", ServiceType: "等保测评", Site: "合同场所", Batch: "B-SUP", Category: "等保测评", System: "权威系统", SystemLevel: "三级", Requirement: "权威要求", TestMode: "STANDARD"},
		},
	}
}

func adjustmentRepositoryFixture() *reviewAdjustmentRepository {
	return &reviewAdjustmentRepository{adjustmentRepository{project: domain.Project{ID: "PJ-1", TenantID: "tenant-1", ContractID: "C-1", CustomerID: "customer", Customer: "示例客户"}}}
}

// 调整清单里出现补充协议目录外的 source_id 时必须拒绝，且不产生调整事件。
func TestDecompositionAdjustmentRejectsSourceOutsideSupplementCatalog(t *testing.T) {
	repository := adjustmentRepositoryFixture()
	service := &Service{Repo: repository, Contracts: supplementVerifier(supplementCatalog(), nil)}
	err := service.AdjustDecomposition(context.Background(), adjustmentPrincipal(), "PJ-1", domain.DecompositionAdjustmentInput{
		Reason: "客户追加范围", SupplementContractID: "SUP-1",
		Items: []domain.ContractService{{SourceID: "SVC-UNKNOWN", Site: "杭州机房"}},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err=%v, want ErrValidation", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("rejected adjustment still emitted events: %+v", repository.events)
	}
}

// 同一补充协议服务项被重复提交时必须拒绝：重复挂同一 source_id 会伪造范围。
func TestDecompositionAdjustmentRejectsDuplicateSourceSelection(t *testing.T) {
	repository := adjustmentRepositoryFixture()
	service := &Service{Repo: repository, Contracts: supplementVerifier(supplementCatalog(), nil)}
	err := service.AdjustDecomposition(context.Background(), adjustmentPrincipal(), "PJ-1", domain.DecompositionAdjustmentInput{
		Reason: "客户追加范围", SupplementContractID: "SUP-1",
		Items: []domain.ContractService{{SourceID: "SVC-1", Site: "杭州机房"}, {SourceID: "SVC-1", Site: "上海机房"}},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err=%v, want ErrValidation", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("duplicate selection still emitted events: %+v", repository.events)
	}
}

// 目录内的 source_id 成功，且批次/类别/体系要求/方法类型来自权威目录；
// 客户端只能补现场字段（场所）。
func TestDecompositionAdjustmentRebuildsItemsFromSupplementCatalog(t *testing.T) {
	repository := adjustmentRepositoryFixture()
	service := &Service{Repo: repository, Contracts: supplementVerifier(supplementCatalog(), nil)}
	err := service.AdjustDecomposition(context.Background(), adjustmentPrincipal(), "PJ-1", domain.DecompositionAdjustmentInput{
		Reason: "客户追加范围", SupplementContractID: "SUP-1",
		Items: []domain.ContractService{{
			SourceID: "SVC-1", Site: "客户现场",
			Batch: "伪造批次", Category: "伪造类别", System: "伪造系统", SystemLevel: "伪造级别",
			Requirement: "伪造要求", TestMode: "PENETRATION",
		}},
	})
	if err != nil {
		t.Fatalf("adjustment failed: %v", err)
	}
	if len(repository.events) != 1 || repository.events[0].Type != EventDecompositionAdjusted {
		t.Fatalf("events=%+v", repository.events)
	}
	items, ok := repository.events[0].Payload["service_items"].([]domain.ServiceItem)
	if !ok || len(items) != 1 {
		t.Fatalf("service_items=%+v", repository.events[0].Payload["service_items"])
	}
	item := items[0]
	if item.SourceServiceID != "SVC-1" || item.Site != "客户现场" {
		t.Fatalf("selection identity/site lost: %+v", item)
	}
	if item.Batch != "B-SUP" || item.Category != "等保测评" || item.System != "权威系统" || item.TestMode != "STANDARD" {
		t.Fatalf("service classification not rebuilt from catalog: %+v", item)
	}
	if got := repository.events[0].Payload["supplement_contract_id"]; got != "SUP-1" {
		t.Fatalf("supplement_contract_id=%v, want SUP-1", got)
	}
}

// 补充协议服务项目录不可用或版本漂移时必须失败关闭（不降级成客户端清单）。
func TestDecompositionAdjustmentFailsClosedWhenCatalogUnavailable(t *testing.T) {
	stale := supplementCatalog()
	stale.ContractVersion = 4
	cases := []struct {
		name    string
		catalog platform.ApprovedContractServiceCatalog
		err     error
		want    error
	}{
		{"catalog unavailable", platform.ApprovedContractServiceCatalog{}, errors.New("contract service down"), ErrContractUnavailable},
		{"catalog version drift", stale, nil, ErrPrecondition},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repository := adjustmentRepositoryFixture()
			service := &Service{Repo: repository, Contracts: supplementVerifier(testCase.catalog, testCase.err)}
			err := service.AdjustDecomposition(context.Background(), adjustmentPrincipal(), "PJ-1", domain.DecompositionAdjustmentInput{
				Reason: "客户追加范围", SupplementContractID: "SUP-1",
				Items: []domain.ContractService{{SourceID: "SVC-1", Site: "客户现场"}},
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err=%v, want %v", err, testCase.want)
			}
			if len(repository.events) != 0 {
				t.Fatalf("fail-closed adjustment still emitted events: %+v", repository.events)
			}
		})
	}
}
