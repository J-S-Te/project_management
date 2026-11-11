package migrations

import (
	"strconv"
	"strings"
	"testing"
)

func TestDeliveryEffectsMigrationKeepsIntentAndRecoveryBoundaries(t *testing.T) {
	body, err := Files.ReadFile("000028_delivery_effects_outbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"CREATE TABLE IF NOT EXISTS pm_delivery_effects_outbox", "UNIQUE KEY uq_pm_effects_source (tenant_id, source_event_id)", "payload JSON NOT NULL", "idx_pm_effects_claim", "locked_until", "last_error_summary", "completed_at"} {
		if !strings.Contains(string(body), expected) {
			t.Fatalf("missing durable boundary %q", expected)
		}
	}
	if strings.Contains(string(body), "INSERT INTO") {
		t.Fatal("historical events must not be retroactively replayed")
	}
}

func TestIncomeContractCategoriesOnlyAddMissingTenantEntries(t *testing.T) {
	body, err := Files.ReadFile("000027_income_contract_service_categories.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, want := range []string{"模块开发", "技术咨询", "WHERE NOT EXISTS", "existing.tenant_id = tenants.tenant_id", "existing.category = categories.category", "UNION SELECT tenant_id FROM pm_project"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing migration boundary %q", want)
		}
	}
	if strings.Contains(sql, "UPDATE pm_detection_category") || strings.Contains(sql, "DELETE FROM") {
		t.Fatal("migration must not rewrite existing categories")
	}
}

func TestOIDCSessionMigrationPreservesReplayAuditAndIdentityRevocationIndex(t *testing.T) {
	body, err := Files.ReadFile("000003_project_oidc_sessions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"state_hash BINARY(32)", "consumed_at DATETIME(3) NULL", "session_id_hash BINARY(32)", "principal_json JSON", "id_token_ciphertext MEDIUMBLOB", "oauth_token_ciphertext MEDIUMBLOB", "idx_pm_oidc_session_subject (tenant_id, identity_id, revoked_at)"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestProjectScopeMigrationAddsStableOwnerBoundaries(t *testing.T) {
	body, err := Files.ReadFile("000004_project_data_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{"owner_org_id", "owner_identity_id", "manager_identity_id", "idx_pm_project_tenant_owner_org", "idx_pm_project_tenant_owner_identity"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestBackchannelLogoutMigrationAddsSIDAndReplayBoundary(t *testing.T) {
	body, err := Files.ReadFile("000006_oidc_backchannel_logout.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"oidc_subject VARCHAR(128)", "oidc_session_id VARCHAR(128)",
		"idx_pm_oidc_session_sid", "pm_oidc_backchannel_logout_replay", "PRIMARY KEY (jti)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("back-channel logout migration missing %q", required)
		}
	}
}

func TestServiceItemSystemLevelMigration(t *testing.T) {
	body, err := Files.ReadFile("000007_project_service_system_level.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ADD COLUMN system_level VARCHAR(64) NOT NULL DEFAULT ''") {
		t.Fatal("system level migration is missing")
	}
}

func TestProjectGovernanceMigrationAddsReviewReportAndComplianceStructures(t *testing.T) {
	body, err := Files.ReadFile("000008_project_governance.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		// 服务项治理列必须幂等补齐：先查 information_schema 再执行 ALTER，
		// 因为 MySQL 的 DDL 会隐式提交，早期失败的库可能已经加过这些列。
		"information_schema.columns",
		"ADD COLUMN tech_review_status VARCHAR(32) NOT NULL DEFAULT ''NONE'' AFTER conflict_status",
		"ADD COLUMN report_status VARCHAR(32) NOT NULL DEFAULT ''NONE'' AFTER tech_review_comment",
		"CREATE TABLE IF NOT EXISTS pm_impl_plan",
		"auth_doc_no VARCHAR(128)",
		"auth_scope TEXT",
		"emergency_contact VARCHAR(128)",
		"rollback_plan TEXT",
		// TEXT 列不能用字面量默认值（MySQL 1101），必须使用表达式默认值。
		"penetration_test_plan TEXT NOT NULL DEFAULT ('')",
		"CREATE TABLE IF NOT EXISTS pm_split_rule",
		"CREATE TABLE IF NOT EXISTS pm_warning_rule",
		"CREATE TABLE IF NOT EXISTS pm_automation",
		"CREATE TABLE IF NOT EXISTS pm_field_permission",
		"CREATE TABLE IF NOT EXISTS pm_sla",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("governance migration missing %q", required)
		}
	}
	if strings.Contains(sql, "TEXT NOT NULL DEFAULT ''") {
		t.Fatal("governance migration uses a literal TEXT default, which MySQL rejects with error 1101")
	}
}

// 人员资质档案必须能回基础平台复核：identity_status 记录复核结论，
// identity_checked_at 记录复核时间；未关联平台账号的历史档案保持 UNLINKED。
func TestPersonnelIdentityMigrationAddsReviewColumns(t *testing.T) {
	body, err := Files.ReadFile("000013_personnel_identity_check.sql")
	if err != nil {
		t.Fatal(err)
	}
	content := string(body)
	for _, expected := range []string{
		"ALTER TABLE pm_capability ADD COLUMN identity_status VARCHAR(16) NOT NULL DEFAULT 'UNLINKED'",
		"ALTER TABLE pm_capability ADD COLUMN identity_checked_at DATETIME(3) NULL",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("migration missing %q:\n%s", expected, content)
		}
	}
}

// 站点台账：site 原本只是从合同复制的自由文本，既无法聚合也没有坐标；
// pm_site 把它提升为主数据，坐标用可空列区分"未采集"与"坐标为 0"。
func TestSiteRegistryMigrationCreatesCoordinateAwareTable(t *testing.T) {
	body, err := Files.ReadFile("000014_site_registry.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS pm_site",
		"site_code VARCHAR(64) NOT NULL",
		"latitude DECIMAL(10,7) NULL",
		"longitude DECIMAL(10,7) NULL",
		"UNIQUE KEY uq_pm_site_tenant_code (tenant_id, site_code)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

// 合同拆解规则配置 v2：三块配置表 + 服务项体系要求字段 + 存量自由文本规则清理。
func TestSplitRuleConfigMigration(t *testing.T) {
	body, err := Files.ReadFile("000017_split_rule_config_v2.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"pm_split_policy", "pm_detection_category", "pm_split_override",
		"dimension_primary", "dimension_secondary", "default_status",
		"missing_rule_action", "scope_change_detection",
		"required_qualifications", "special_method",
		"match_conditions JSON", "override_settings JSON", "priority INT",
		"ADD COLUMN system_standard VARCHAR(128) NOT NULL DEFAULT ''",
		"DELETE FROM pm_split_rule WHERE kind = 'split-rules'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("split rule config migration missing %q", required)
		}
	}
	// 已应用的迁移不得再改：第三维用独立迁移追加。
	tertiary, err := Files.ReadFile("000018_split_policy_tertiary_dimension.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tertiary), "dimension_tertiary VARCHAR(32) NOT NULL DEFAULT ''") {
		t.Fatal("tertiary dimension migration is missing")
	}
	// 必检能力码：检测类别域的「必备资质」是资质名称，能力校验比对的是能力码，
	// 两者不是同一套编码，因此单独给出能力码列（默认留空，避免凭空造码导致分配一律冲突）。
	codes, err := Files.ReadFile("000019_detection_category_required_codes.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codes), "required_codes VARCHAR(255) NOT NULL DEFAULT ''") {
		t.Fatal("required codes migration is missing")
	}
}

func TestRuleConfigurationConstraintMigrationPreservesHistoryAndPreventsActiveDuplicates(t *testing.T) {
	body, err := Files.ReadFile("000023_rule_configuration_constraints.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"UPDATE pm_automation AS duplicate_rule",
		"uq_pm_automation_active_condition",
		"UPDATE pm_field_permission AS duplicate_rule",
		"uq_pm_field_permission_active_condition",
		"UPDATE pm_sla AS duplicate_rule",
		"uq_pm_sla_active_status",
		"GENERATED ALWAYS AS (CASE WHEN enabled = TRUE",
		"duplicate_rule.updated_by = 'migration-000023'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("rule constraint migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "DELETE FROM") {
		t.Fatal("duplicate rule migration must preserve historical rows instead of deleting them")
	}
}

func TestContractIdentityMigrationUsesStableIDForIdempotency(t *testing.T) {
	body, err := Files.ReadFile("000024_contract_identity_key.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"DROP INDEX uq_pm_project_contract_version",
		"GENERATED ALWAYS AS (CASE WHEN contract_id <> '' THEN contract_id ELSE contract END) STORED",
		"ADD UNIQUE KEY uq_pm_project_contract_version (tenant_id, contract_identity_key, contract_version)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("contract identity migration missing %q", required)
		}
	}
}

// A04：普通行版本只用于并发/审计，建项唯一性改为「租户 + 合同身份」。
// 迁移必须 失败时安全拒绝：存量已有同合同多项目时以明确错误停止，绝不删除/合并历史项目。
func TestContractProjectIdentityMigrationFailsClosedOnExistingDuplicates(t *testing.T) {
	body, err := Files.ReadFile("000033_contract_project_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"DROP INDEX uq_pm_project_contract_version",
		"ADD UNIQUE KEY uq_pm_project_contract_identity (tenant_id, contract_identity_key)",
		"GROUP BY tenant_id, contract_identity_key",
		// 历史行把合同编号写进 contract_id、新行写稳定 ID 时，按权威合同号再查一次。
		"WHERE contract <> ''",
		"UNION ALL",
		"HAVING COUNT(*) > 1",
		"SIGNAL SQLSTATE '45000'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("contract project identity migration missing %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DELETE FROM", "UPDATE PM_PROJECT", "DROP TABLE", "TRUNCATE"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("fail-closed migration must not rewrite historical projects, found %q", forbidden)
		}
	}
	if !strings.Contains(sql, "-- 只读预检") {
		t.Fatal("migration must document the read-only precheck query")
	}
	// 守卫必须先于唯一键变更执行：存量冲突时不得先改结构再报错。
	guard, alter := strings.Index(sql, "CALL pm_guard_contract_project_identity();"), strings.Index(sql, "ALTER TABLE pm_project")
	if guard < 0 || alter < 0 || guard > alter {
		t.Fatalf("fail-closed guard must run before the unique-key change: guard=%d alter=%d", guard, alter)
	}
}

// 原生结构守卫；执行及中断恢复仍须隔离 MySQL。
func TestContractProjectIdentityMigrationCanResumeAfterDDLBeforeLedger(t *testing.T) {
	body, err := Files.ReadFile("000033_contract_project_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, guard := range []string{"information_schema.statistics", "IF identity_index_count = 0 THEN", "IF version_index_count > 0 THEN", "identity_index_columns <> 'tenant_id,contract_identity_key'", "index definition mismatch"} {
		if !strings.Contains(sql, guard) {
			t.Fatalf("DDL interruption recovery missing %q", guard)
		}
	}
	if strings.Index(sql, "IF duplicate_groups > 0 THEN") > strings.Index(sql, "IF identity_index_count = 0 THEN") {
		t.Fatal("duplicate preflight must run before any index DDL, including retries")
	}
}

// 资质/能力编码使用独立的租户目录，并从存量能力档案回填，
// 避免上线后旧编码因未手工重录而立即失效。
func TestCapabilityCodeCatalogMigration(t *testing.T) {
	body, err := Files.ReadFile("000021_capability_code_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS pm_capability_code",
		"scope VARCHAR(128) NOT NULL",
		"check_type VARCHAR(16) NOT NULL",
		"UNIQUE KEY uk_pm_capability_code_tenant_type_code (tenant_id, check_type, scope)",
		"JOIN JSON_TABLE(",
		"capability.capability_codes",
		"UPPER(TRIM(code_row.code))",
		"UPPER(TRIM(capability.resource_type)) IN ('PERSON', 'EQUIPMENT')",
		"FROM pm_service_item AS service_item",
		"COALESCE(service_item.required_codes, JSON_ARRAY())",
		"'PERSON'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("capability code migration missing %q", required)
		}
	}
}

func TestEmbeddedPenetrationWorkPackageMigrationSeparatesSubjectAndIdempotency(t *testing.T) {
	body, err := Files.ReadFile("000025_penetration_work_package.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE pm_penetration_work_package",
		"UNIQUE KEY uq_pm_penetration_parent (tenant_id, parent_service_item_id)",
		"CREATE TABLE pm_penetration_work_package_event",
		"UNIQUE KEY uq_pm_penetration_event_idempotency (tenant_id, idempotency_key)",
		"decision_status IN ('PENDING','REQUIRED','NOT_REQUIRED')",
		"subject_type VARCHAR(32) NOT NULL DEFAULT 'SERVICE_ITEM'",
		"UPDATE pm_report_revision SET subject_id = service_item_id",
		"uq_pm_report_revision_subject (tenant_id, subject_type, subject_id, revision)",
		"'PENETRATION_WORK_PACKAGE'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("embedded penetration migration missing %q", required)
		}
	}
	if strings.Contains(sql, "ALTER TABLE pm_service_item ADD") {
		t.Fatal("embedded penetration must not be represented by expanding the service-item count")
	}
}

// TestOIDCSessionIdleMigrationAddsNullableInteractiveTimestamp 固定交互列的可空兼容语义。
// 参数：t 为测试上下文。
// 返回值：无；迁移改写既有行或绝对截止时失败。
func TestOIDCSessionIdleMigrationAddsNullableInteractiveTimestamp(t *testing.T) {
	body, err := Files.ReadFile("000038_pm_oidc_session_idle.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"ALTER TABLE pm_oidc_session",
		"ADD COLUMN last_interactive_at DATETIME(3) NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("idle session migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE ", "DELETE ", "INSERT INTO", "session_expires_at", "DROP ", "MODIFY "} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("idle session migration must not touch existing rows or deadlines: found %q", forbidden)
		}
	}
}

// TestAuditOutboxRequestIDMigrationMatchesReadModel 固定审计请求关联列的增量迁移。
// 参数：t 为测试上下文。
// 返回值：无；列重复声明、非可空或历史数据改写时失败。
func TestAuditOutboxRequestIDMigrationMatchesReadModel(t *testing.T) {
	body, err := Files.ReadFile("000039_pm_audit_request_id.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"ALTER TABLE prj_audit_outbox",
		"ADD COLUMN request_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL",
		"AFTER resource_id",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("audit request_id migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE ", "DELETE ", "INSERT INTO", "DROP ", "MODIFY ", "NOT NULL", "CHANGE "} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("audit request_id migration must stay additive and nullable: found %q", forbidden)
		}
	}
	// 同一列不得被其它迁移重复添加：MySQL 会以 1060 duplicate column 失败。
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() == "000039_pm_audit_request_id.sql" {
			continue
		}
		other, err := Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(other), "prj_audit_outbox") && strings.Contains(string(other), "request_id") {
			t.Fatalf("%s already declares prj_audit_outbox.request_id", entry.Name())
		}
	}
}

func TestOfflineFieldEvidenceMigrationAddsReplayAndCaptureBoundaries(t *testing.T) {
	body, err := Files.ReadFile("000026_offline_field_evidence.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"client_operation_id VARCHAR(128) NULL",
		"captured_at DATETIME(3) NULL",
		"UNIQUE KEY uq_pm_event_offline_operation",
		"(tenant_id, service_item_id, actor_user_id, client_operation_id)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("offline field migration missing %q", required)
		}
	}
}

// TestEmbeddedMigrationVersionsRemainUniqueAndContiguous 固定合并后迁移版本连续且不重复。
// 参数：t 为测试上下文。
// 返回值：无；版本重复、缺口或命名异常时失败。
func TestEmbeddedMigrationVersionsRemainUniqueAndContiguous(t *testing.T) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	expected := 1
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version != expected {
			t.Fatalf("migration %s version=%d want=%d err=%v", entry.Name(), version, expected, err)
		}
		expected++
	}
	if expected != 48 {
		t.Fatalf("migration count=%d want=47", expected-1)
	}
}

// TestImportOperationMigrationKeepsIdempotencyAndReplayBoundaries 固定导入确认幂等台账的结构：
// 同租户同键唯一（并发兜底）、请求指纹与响应快照必须落库，且迁移只新增表、不改写历史数据。
// 参数：t 为测试上下文。
// 返回值：无；缺少唯一键、请求指纹或响应快照列时失败。
func TestImportOperationMigrationKeepsIdempotencyAndReplayBoundaries(t *testing.T) {
	body, err := Files.ReadFile("000043_import_operation.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS pm_import_operation",
		"tenant_id VARCHAR(64) NOT NULL",
		"idempotency_key VARCHAR(128) NOT NULL",
		"operation_type VARCHAR(64) NOT NULL",
		"request_hash CHAR(64) NOT NULL",
		"response_json LONGTEXT NOT NULL",
		"created_at DATETIME(3) NOT NULL",
		"UNIQUE KEY uq_pm_import_operation_key (tenant_id, idempotency_key)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("import operation migration missing %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DELETE FROM", "UPDATE PM_", "DROP TABLE", "TRUNCATE", "ALTER TABLE PM_CAPABILITY"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("import operation migration must stay additive, found %q", forbidden)
		}
	}
	// 唯一键必须与响应快照处于同一张表：先声明列再声明唯一键，重放才能读到首次结果。
	uniqueKey := strings.Index(sql, "UNIQUE KEY uq_pm_import_operation_key")
	responseColumn := strings.Index(sql, "response_json LONGTEXT NOT NULL")
	if uniqueKey < 0 || responseColumn < 0 || uniqueKey < responseColumn {
		t.Fatal("unique key must be declared after the replay columns in the same table definition")
	}
}

// TestAuditOutboxDeliveredIndexMigrationIsAdditiveAndIdempotent 固定已投递行清理索引的边界：
// 索引列必须与 PurgeDelivered 的清理谓词同序（等值列在前、范围/排序列在后），
// 迁移必须按 information_schema 判重入且只加索引，不改写任何历史行。
// 参数：t 为测试上下文。
// 返回值：无；缺少判重入结构或出现数据改写语句时失败。
func TestAuditOutboxDeliveredIndexMigrationIsAdditiveAndIdempotent(t *testing.T) {
	body, err := Files.ReadFile("000044_prj_audit_outbox_delivered_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"information_schema.STATISTICS",
		"TABLE_NAME = 'prj_audit_outbox'",
		"INDEX_NAME = 'idx_prj_audit_outbox_delivered'",
		"ADD INDEX idx_prj_audit_outbox_delivered (delivery_status, delivered_at)",
		"SIGNAL SQLSTATE '45000'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("delivered index migration missing %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DELETE FROM", "UPDATE PRJ_AUDIT_OUTBOX", "DROP TABLE", "TRUNCATE", "DROP COLUMN"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("delivered index migration must stay additive, found %q", forbidden)
		}
	}
}
