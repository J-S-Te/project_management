package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

func TestNormalizeCapabilityClearsOnlyPersonnelValidityDates(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	person := domain.Capability{ResourceType: "person", ValidFrom: from, ValidUntil: until}
	normalizeCapability(&person)
	if !person.ValidFrom.IsZero() || !person.ValidUntil.IsZero() {
		t.Fatalf("personnel dates were retained: %+v", person)
	}
	equipment := domain.Capability{ResourceType: "equipment", ValidFrom: from, ValidUntil: until}
	normalizeCapability(&equipment)
	if !equipment.ValidFrom.Equal(from) || !equipment.ValidUntil.Equal(until) {
		t.Fatalf("equipment calibration dates were removed: %+v", equipment)
	}
}

type capabilityCodeRuleRepository struct {
	scopeRepository
	rules      []domain.Rule
	references int64
}

func (r *capabilityCodeRuleRepository) ListRules(_ context.Context, _ string, kind string) ([]domain.Rule, error) {
	if kind != capabilityCodeRuleKind {
		return nil, nil
	}
	return append([]domain.Rule(nil), r.rules...), nil
}

func (r *capabilityCodeRuleRepository) CreateRule(_ context.Context, item domain.Rule) (domain.Rule, error) {
	item.ID = int64(len(r.rules) + 1)
	r.rules = append(r.rules, item)
	return item, nil
}

func (r *capabilityCodeRuleRepository) UpdateRule(_ context.Context, _ string, _ string, id int64, item domain.Rule) (domain.Rule, error) {
	for index := range r.rules {
		if r.rules[index].ID == id {
			item.ID = id
			r.rules[index] = item
			return item, nil
		}
	}
	return domain.Rule{}, ErrNotFound
}

func (r *capabilityCodeRuleRepository) DeleteRule(_ context.Context, _ string, kind string, id int64) (domain.Rule, error) {
	for index := range r.rules {
		if r.rules[index].Kind == kind && r.rules[index].ID == id {
			removed := r.rules[index]
			r.rules = append(r.rules[:index], r.rules[index+1:]...)
			return removed, nil
		}
	}
	return domain.Rule{}, ErrNotFound
}

func (r *capabilityCodeRuleRepository) CountCapabilityCodeReferences(context.Context, string, string, string) (int64, error) {
	return r.references, nil
}

func TestCapabilityCodeRuleNormalizesRejectsDuplicatesAndKeepsIdentityImmutable(t *testing.T) {
	repo := &capabilityCodeRuleRepository{}
	service := &Service{Repo: repo}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "quality_manager", ScopeType: "APPLICATION"})

	created, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: capabilityCodeRuleKind, Name: " 注册信息安全专业人员 ", Scope: " cisp ", CheckType: " person ", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Scope != "CISP" || created.CheckType != "PERSON" || created.Name != "注册信息安全专业人员" {
		t.Fatalf("created=%+v", created)
	}
	if _, err := service.CreateRule(context.Background(), principal, domain.Rule{
		Kind: capabilityCodeRuleKind, Name: "重复", Scope: "CiSp", CheckType: "PERSON", Enabled: true,
	}); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("duplicate error=%v", err)
	}
	updated, err := service.UpdateRule(context.Background(), principal, created.ID, domain.Rule{
		Kind: capabilityCodeRuleKind, Name: "CISP 新名称", Scope: " cisp ", CheckType: "person", Enabled: false,
	})
	if err != nil || updated.Name != "CISP 新名称" || updated.Enabled {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := service.UpdateRule(context.Background(), principal, created.ID, domain.Rule{
		Kind: capabilityCodeRuleKind, Name: "改码", Scope: "CISP-PTE", CheckType: "PERSON", Enabled: true,
	}); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "不可修改") {
		t.Fatalf("identity mutation error=%v", err)
	}
}

func TestCapabilityCodeRuleCannotDeleteReferencedCode(t *testing.T) {
	repo := &capabilityCodeRuleRepository{rules: []domain.Rule{{ID: 7, Kind: capabilityCodeRuleKind, Name: "CISP", Scope: "CISP", CheckType: "PERSON", Enabled: true}}, references: 2}
	service := &Service{Repo: repo}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "quality_manager", ScopeType: "APPLICATION"})
	_, err := service.DeleteRule(context.Background(), principal, capabilityCodeRuleKind, 7)
	if !errors.Is(err, ErrResourceConflict) || !strings.Contains(err.Error(), "请改为禁用") {
		t.Fatalf("delete referenced code error=%v", err)
	}
	if len(repo.rules) != 1 {
		t.Fatal("referenced code was deleted")
	}
}

func TestCapabilityCodeRuleDeleteFailsClosedWithoutReferenceChecker(t *testing.T) {
	repo := &scopeRepository{}
	service := &Service{Repo: repo}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "quality_manager", ScopeType: "APPLICATION"})
	// scopeRepository 的目录返回为空，因此先用一个只提供目录、但不提供引用检查器的包装仓储。
	withoutChecker := &capabilityCodeNoReferenceRepository{scopeRepository: *repo, rules: []domain.Rule{{ID: 9, Kind: capabilityCodeRuleKind, Scope: "CISP", CheckType: "PERSON"}}}
	service.Repo = withoutChecker
	if _, err := service.DeleteRule(context.Background(), principal, capabilityCodeRuleKind, 9); err == nil || !strings.Contains(err.Error(), "reference repository unavailable") {
		t.Fatalf("delete without checker error=%v", err)
	}
}

type capabilityCodeNoReferenceRepository struct {
	scopeRepository
	rules []domain.Rule
}

func (r *capabilityCodeNoReferenceRepository) ListRules(context.Context, string, string) ([]domain.Rule, error) {
	return r.rules, nil
}

func TestCapabilityCodeRuleRejectsOversizedAndControlCharacterInputs(t *testing.T) {
	for name, item := range map[string]domain.Rule{
		"name too long": {Name: strings.Repeat("名", 256), Scope: "CISP", CheckType: "PERSON"},
		"code too long": {Name: "长编码", Scope: strings.Repeat("A", 129), CheckType: "PERSON"},
		"control char":  {Name: "错误\n名称", Scope: "CISP", CheckType: "PERSON"},
	} {
		t.Run(name, func(t *testing.T) {
			item.Kind = capabilityCodeRuleKind
			if err := normalizeCapabilityCodeRule(&item); !errors.Is(err, ErrValidation) {
				t.Fatalf("error=%v, want validation", err)
			}
		})
	}
}

func TestCapabilityWritesRequireEnabledCatalogButPreserveHistoricalCodes(t *testing.T) {
	manager := principalWith("project.device.manage", platform.DataScope{RoleCode: "device_admin", ScopeType: "APPLICATION"})

	t.Run("new equipment fails closed without a catalog", func(t *testing.T) {
		repo := &capabilityRepository{rules: []domain.Rule{}}
		service := &Service{Repo: repo}
		_, err := service.UpsertEquipment(context.Background(), manager, domain.Capability{ResourceType: "equipment", ResourceID: "EQ-NEW", ResourceName: "新设备", Codes: []string{"new-code"}})
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "尚未配置设备能力编码目录") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("existing disabled code can remain but cannot spread", func(t *testing.T) {
		repo := &capabilityRepository{
			capabilities: []domain.Capability{{ResourceType: "EQUIPMENT", ResourceID: "EQ-1", ResourceName: "旧设备", Codes: []string{"LEGACY"}}},
			rules: []domain.Rule{
				{Kind: capabilityCodeRuleKind, Scope: "LEGACY", CheckType: "EQUIPMENT", Enabled: false},
				{Kind: capabilityCodeRuleKind, Scope: "ACTIVE-CODE", CheckType: "EQUIPMENT", Enabled: true},
			},
		}
		service := &Service{Repo: repo}
		saved, err := service.UpsertEquipment(context.Background(), manager, domain.Capability{ResourceType: " equipment ", ResourceID: "EQ-1", ResourceName: "旧设备改名", Codes: []string{" legacy ", "active-code", "ACTIVE-CODE"}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(saved.Codes, ",") != "LEGACY,ACTIVE-CODE" {
			t.Fatalf("normalized codes=%v", saved.Codes)
		}
		_, err = service.UpsertEquipment(context.Background(), manager, domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-2", ResourceName: "新设备", Codes: []string{"LEGACY"}})
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "未在系统配置中启用") {
			t.Fatalf("disabled code spread error=%v", err)
		}
	})
}

func TestCapabilityUpsertAndImportApplySameCatalogValidation(t *testing.T) {
	rules := []domain.Rule{{Kind: capabilityCodeRuleKind, Scope: "EQ-A", CheckType: "EQUIPMENT", Enabled: true}}
	repo := &capabilityRepository{rules: rules}
	service := &Service{Repo: repo}
	manager := principalWith("project.resource.manage", platform.DataScope{RoleCode: "quality_manager", ScopeType: "APPLICATION"})

	saved, err := service.UpsertCapability(context.Background(), manager, domain.Capability{ResourceType: "equipment", ResourceID: "EQ-1", ResourceName: "扫描器", Codes: []string{" eq-a "}})
	if err != nil || len(saved.Codes) != 1 || saved.Codes[0] != "EQ-A" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	result, err := service.ImportCapabilities(context.Background(), manager, []domain.Capability{
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-2", ResourceName: "有效", Codes: []string{"EQ-A"}},
		{ResourceType: "EQUIPMENT", ResourceID: "EQ-3", ResourceName: "无效", Codes: []string{"EQ-X"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Skipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "EQ-X") {
		t.Fatalf("result=%+v", result)
	}
}

type capabilityCodeSplitRepository struct {
	capabilityCodeRuleRepository
	saved []domain.DetectionCategory
}

func (r *capabilityCodeSplitRepository) GetSplitPolicy(context.Context, string) (domain.SplitPolicy, error) {
	return domain.DefaultSplitPolicy(), nil
}
func (r *capabilityCodeSplitRepository) SaveSplitPolicy(_ context.Context, _ string, item domain.SplitPolicy, _ string) (domain.SplitPolicy, error) {
	return item, nil
}
func (r *capabilityCodeSplitRepository) ListDetectionCategories(context.Context, string) ([]domain.DetectionCategory, error) {
	return r.saved, nil
}
func (r *capabilityCodeSplitRepository) SaveDetectionCategory(_ context.Context, _ string, item domain.DetectionCategory, _ string) (domain.DetectionCategory, error) {
	r.saved = append(r.saved, item)
	return item, nil
}
func (r *capabilityCodeSplitRepository) DeleteDetectionCategory(context.Context, string, string) error {
	return nil
}
func (r *capabilityCodeSplitRepository) ListSplitOverrides(context.Context, string) ([]domain.SplitOverride, error) {
	return nil, nil
}
func (r *capabilityCodeSplitRepository) SaveSplitOverride(_ context.Context, _ string, item domain.SplitOverride, _ string) (domain.SplitOverride, error) {
	return item, nil
}
func (r *capabilityCodeSplitRepository) DeleteSplitOverride(context.Context, string, int64) (domain.SplitOverride, error) {
	return domain.SplitOverride{}, nil
}

func TestDetectionCategoryRequiredCodesOnlyAcceptEnabledPersonQualifications(t *testing.T) {
	repo := &capabilityCodeSplitRepository{capabilityCodeRuleRepository: capabilityCodeRuleRepository{rules: []domain.Rule{
		{Kind: capabilityCodeRuleKind, Scope: "PERSON-A", CheckType: "PERSON", Enabled: true},
		{Kind: capabilityCodeRuleKind, Scope: "PERSON-OFF", CheckType: "PERSON", Enabled: false},
		{Kind: capabilityCodeRuleKind, Scope: "DEVICE-A", CheckType: "EQUIPMENT", Enabled: true},
	}}}
	service := &Service{Repo: repo}
	manager := principalWith("project_rule.manage", platform.DataScope{RoleCode: "quality_manager", ScopeType: "APPLICATION"})

	saved, err := service.SaveDetectionCategory(context.Background(), manager, domain.DetectionCategory{Category: "等保", RequiredCodes: " person-a ", SpecialMethod: domain.SpecialMethodNo, Enabled: true})
	if err != nil || saved.RequiredCodes != "PERSON-A" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	for _, invalid := range []string{"PERSON-OFF", "DEVICE-A"} {
		_, err := service.SaveDetectionCategory(context.Background(), manager, domain.DetectionCategory{Category: "无效-" + invalid, RequiredCodes: invalid, SpecialMethod: domain.SpecialMethodNo, Enabled: true})
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), invalid) {
			t.Fatalf("required code %s error=%v", invalid, err)
		}
	}
	result, err := service.ImportDetectionCategories(context.Background(), manager, []domain.DetectionCategory{
		{Category: "导入有效", RequiredCodes: "person-a", SpecialMethod: domain.SpecialMethodNo, Enabled: true},
		{Category: "导入无效", RequiredCodes: "device-a", SpecialMethod: domain.SpecialMethodNo, Enabled: true},
	})
	if err != nil || result.Imported != 1 || result.Skipped != 1 || len(result.Errors) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
