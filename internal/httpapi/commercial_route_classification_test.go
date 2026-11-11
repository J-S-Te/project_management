package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
)

// registeredGETOperations 是"每条注册的 GET 路由都被显式分类"的清单。
//
// 许可到期只读期由 commercialOperation 决定放行还是 403，而 GA 路由表本身没有
// 读/写元数据：GET 一旦漏进白名单就会静默落到默认值 MUTATE_BUSINESS，前端
// fail-closed 会把只读页面判成"授权不可用"（PRJ-01 即此问题）。因此这里为每条
// 注册 GET 点名期望分类，并由 TestRegisteredGETRoutesHaveExplicitCommercialClassification
// 与真实路由表双向比对：漏加、改名、参数占位写错、清理路由后留下的陈旧条目都会失败。
//
// 取值语义：
//   - core.READ_HISTORY / core.EXPORT_HISTORY / core.ESSENTIAL_SERVICE：到期仍放行的只读能力；
//   - core.MUTATE_BUSINESS：该 GET 实际带副作用，到期必须 403（当前没有此类 GET，
//     若将来出现必须在这里显式登记并说明副作用）。
var registeredGETOperations = map[string]core.Operation{
	"/api/v1/projects/:id/expense-participants":                 core.READ_HISTORY,
	"/api/v1/projects/:id/reimbursement-spend":                  core.READ_HISTORY,
	"/internal/v1/reimbursement/spend-snapshots/reconciliation": core.READ_HISTORY,
	"/api/v1/projects/:id/native-scope":                         core.READ_HISTORY,
	// 存活、就绪与会话服务：不因商业授权到期而阻断登录/续期/巡检。
	"/healthz":       core.ESSENTIAL_SERVICE,
	"/readyz":        core.ESSENTIAL_SERVICE,
	"/auth/login":    core.ESSENTIAL_SERVICE,
	"/auth/callback": core.ESSENTIAL_SERVICE,
	"/auth/logout":   core.ESSENTIAL_SERVICE,
	"/logged-out":    core.ESSENTIAL_SERVICE,

	// 受控机器读取（结算引用、合同导入、合同集成、交接、数据看板）。
	"/internal/v1/settlement/project-references/:projectID":                 core.READ_HISTORY,
	"/internal/v1/contract-imports/capabilities":                            core.READ_HISTORY,
	"/internal/v1/contract-imports/project-bindings/:origin_import_item_id": core.READ_HISTORY,
	"/internal/v1/contracts/detection-categories":                           core.READ_HISTORY,
	"/internal/v1/handover/responsibilities":                                core.READ_HISTORY,
	"/internal/v1/handover/transfers/:item_id":                              core.READ_HISTORY,
	"/internal/v1/dashboard":                                                core.READ_HISTORY,
	"/internal/v1/dashboard/reports":                                        core.READ_HISTORY,
	"/internal/v1/dashboard/projects/snapshot":                              core.READ_HISTORY,

	// 用户会话与审计只读入口。
	"/api/v1/auth/me":             core.ESSENTIAL_SERVICE,
	"/api/v1/audit-outbox/failed": core.ESSENTIAL_SERVICE,

	// 业务目录、看板与历史读取。
	"/api/v1/navigation":                                                  core.READ_HISTORY,
	"/api/v1/role-catalog":                                                core.READ_HISTORY,
	"/api/v1/rule-configuration-catalog":                                  core.READ_HISTORY,
	"/api/v1/project-creation-capabilities":                               core.READ_HISTORY,
	"/api/v1/dashboard":                                                   core.READ_HISTORY,
	"/api/v1/projects":                                                    core.READ_HISTORY,
	"/api/v1/projects-monitoring":                                         core.READ_HISTORY,
	"/api/v1/approved-contracts":                                          core.READ_HISTORY,
	"/api/v1/approved-contract-options":                                   core.READ_HISTORY,
	"/api/v1/projects/:id/supplement-contracts":                           core.READ_HISTORY,
	"/api/v1/approved-contracts/:contractID/service-items":                core.READ_HISTORY,
	"/api/v1/projects/:id":                                                core.READ_HISTORY,
	"/api/v1/delivery-events":                                             core.READ_HISTORY,
	"/api/v1/delivery/sla-overdue":                                        core.READ_HISTORY,
	"/api/v1/service-items":                                               core.READ_HISTORY,
	"/api/v1/service-items/:id/field-evidence":                            core.READ_HISTORY,
	"/api/v1/service-items/:id/penetration-work-package":                  core.READ_HISTORY,
	"/api/v1/service-items/:id/penetration-work-package/report-revisions": core.READ_HISTORY,
	"/api/v1/service-items/:id/report-revisions":                          core.READ_HISTORY,
	"/api/v1/service-items/:id/equipment-reservations":                    core.READ_HISTORY,
	"/api/v1/personnel":                                                   core.READ_HISTORY,
	"/api/v1/qualified-personnel":                                         core.READ_HISTORY,
	"/api/v1/personnel/names":                                             core.READ_HISTORY,
	"/api/v1/capabilities":                                                core.READ_HISTORY,
	"/api/v1/capabilities/import/template":                                core.READ_HISTORY,
	"/api/v1/capabilities/export":                                         core.EXPORT_HISTORY,
	"/api/v1/equipment":                                                   core.READ_HISTORY,
	"/api/v1/equipment/import/template":                                   core.READ_HISTORY,
	"/api/v1/sites":                                                       core.READ_HISTORY,
	"/api/v1/rules":                                                       core.READ_HISTORY,
	"/api/v1/split-policy":                                                core.READ_HISTORY,
	"/api/v1/detection-categories":                                        core.READ_HISTORY,
	"/api/v1/detection-categories/import/template":                        core.READ_HISTORY,
	"/api/v1/split-overrides":                                             core.READ_HISTORY,
}

// classificationOIDCFlow 让 /auth/* 也进入路由表；未实现 OIDCFlow 的装配不会注册这些路由，
// 清单也就无法覆盖它们。
type classificationOIDCFlow struct{ projectLicenseIdentity }

func (classificationOIDCFlow) Login(http.ResponseWriter, *http.Request)             {}
func (classificationOIDCFlow) Callback(http.ResponseWriter, *http.Request)          {}
func (classificationOIDCFlow) Logout(http.ResponseWriter, *http.Request)            {}
func (classificationOIDCFlow) LogoutLocal(http.ResponseWriter, *http.Request)       {}
func (classificationOIDCFlow) BackchannelLogout(http.ResponseWriter, *http.Request) {}

// fullCommercialRouteTable 打开全部可选分组以核对完整真实路由分类。
// 参数：t 为测试上下文。
// 返回值：包含所有受控接口的路由。
func fullCommercialRouteTable(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	actor := platform.Principal{TenantID: "tenant-1", UserID: "viewer"}
	verifier := handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "tenant-1"}}
	return NewRouter(&application.Service{}, classificationOIDCFlow{projectLicenseIdentity{&actor}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{
		Reimbursement:        &ReimbursementOptions{Enabled: true, ReferenceVerifier: verifier, SpendVerifier: verifier},
		SettlementReference:  &SettlementReferenceOptions{Enabled: true, BearerVerifier: verifier},
		ContractImport:       &ContractImportOptions{Enabled: true, BearerVerifier: verifier},
		ContractIntegration:  &ContractIntegrationOptions{Enabled: true, BearerVerifier: verifier},
		HandoverIntegration:  &HandoverIntegrationOptions{Enabled: true, BearerVerifier: verifier},
		DashboardIntegration: &DashboardIntegrationOptions{Enabled: true, BearerVerifier: verifier},
	})
}

// TestRegisteredGETRoutesHaveExplicitCommercialClassification 防止再出现"只读 GET 漏分类、
// 到期只读期被 403"的回归：路由表可遍历，因此逐条断言注册的 GET 都在清单里，
// 且实际分类与清单一致。
func TestRegisteredGETRoutesHaveExplicitCommercialClassification(t *testing.T) {
	router := fullCommercialRouteTable(t)
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		if route.Method != http.MethodGet {
			continue
		}
		registered[route.Path] = true
		expected, ok := registeredGETOperations[route.Path]
		if !ok {
			t.Errorf("GET %s 未显式分类：只读则加入 commercialHistoryRoutes 并在此登记 READ_HISTORY/EXPORT_HISTORY/ESSENTIAL_SERVICE，有副作用则登记 MUTATE_BUSINESS", route.Path)
			continue
		}
		if actual := commercialOperation(http.MethodGet, route.Path); actual != expected {
			t.Errorf("GET %s 实际分类 %s，清单期望 %s", route.Path, actual, expected)
		}
	}
	if len(registered) == 0 {
		t.Fatal("未从路由表枚举到任何 GET 路由，分类断言会静默失效")
	}
	for path := range registeredGETOperations {
		if !registered[path] {
			t.Errorf("清单中的 GET %s 未被注册：路由改名或删除后清单必须同步，否则分类结论已失真", path)
		}
	}
}

// TestCommercialHistoryWhitelistOnlyHoldsRegisteredReadRoutes 校验白名单不反向失真：
// 白名单用 c.FullPath() 精确匹配，参数占位写错（如 :id 写成 :serviceItemID）不会报错，
// 只会让该路径静默落回 MUTATE_BUSINESS。
func TestCommercialHistoryWhitelistOnlyHoldsRegisteredReadRoutes(t *testing.T) {
	router := fullCommercialRouteTable(t)
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		if route.Method == http.MethodGet {
			registered[route.Path] = true
		}
	}
	for path := range commercialHistoryRoutes {
		if !registered[path] {
			t.Errorf("commercialHistoryRoutes 中的 %s 没有对应注册 GET 路由（参数占位或路径拼写不符）", path)
			continue
		}
		if operation := commercialOperation(http.MethodGet, path); operation != core.READ_HISTORY {
			t.Errorf("commercialHistoryRoutes 中的 %s 分类为 %s，期望 READ_HISTORY", path, operation)
		}
	}
}
