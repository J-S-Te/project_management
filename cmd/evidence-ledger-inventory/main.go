// Command evidence-ledger-inventory 只读盘点 pm_evidence_file 的历史绑定异常。
//
// D1 的目标是先把存量情况盘清楚：按 tenant/project 统计缺少服务项级绑定、
// file_id 未绑定、绑定类型非预期的历史记录。本命令只执行 SELECT，不写业务数据、
// 不跑迁移；默认 dry-run，--apply 明确不实现（回填需单独立项审批）。
//
// 连接配置与既有 cmd 同源：读取 MYSQL_DSN（cmd/migrate 通过 config.Load 消费的同一变量），
// 但不加载 OIDC/Temporal 等与盘点无关的必填配置，避免盘点被无关运行参数阻塞。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	_ "github.com/go-sql-driver/mysql"
)

const (
	modeDryRun     = "DRY_RUN"
	defaultFormat  = "table"
	defaultTimeout = 30 * time.Second
	applyRefusal   = "--apply 不实现：D1 只允许只读盘点，证据台账回填需单独立项审批"
	// sampleIDLimit 是每个 tenant/project 分组在输出中暴露的记录 ID 上限：
	// 只给定位样本，不导出整表，也不输出文件名、摘要、客户等敏感字段。
	sampleIDLimit = 5
)

// evidenceKinds 是当前表约束允许的证据类型（migrations/000022）。
var evidenceKinds = []string{"FIELD", "DEVIATION", "REPORT"}

type options struct {
	format  string
	tenant  string
	timeout time.Duration
}

type categoryCounts struct {
	MissingServiceItem int64 `json:"missing_service_item"`
	UnknownServiceItem int64 `json:"unknown_service_item"`
	EmptyFileID        int64 `json:"empty_file_id"`
	UnexpectedKind     int64 `json:"unexpected_kind"`
}

func (c categoryCounts) add(other categoryCounts) categoryCounts {
	return categoryCounts{
		MissingServiceItem: c.MissingServiceItem + other.MissingServiceItem,
		UnknownServiceItem: c.UnknownServiceItem + other.UnknownServiceItem,
		EmptyFileID:        c.EmptyFileID + other.EmptyFileID,
		UnexpectedKind:     c.UnexpectedKind + other.UnexpectedKind,
	}
}

type scanCounts struct {
	Total   int64 `json:"total"`
	Flagged int64 `json:"flagged"`
}

type tenantStat struct {
	TenantID       string         `json:"tenant_id"`
	ScannedRecords int64          `json:"scanned_records"`
	FlaggedRecords int64          `json:"flagged_records"`
	Projects       int            `json:"projects"`
	Categories     categoryCounts `json:"categories"`
}

type groupStat struct {
	TenantID          string         `json:"tenant_id"`
	ProjectID         string         `json:"project_id"`
	FlaggedRecords    int64          `json:"flagged_records"`
	Categories        categoryCounts `json:"categories"`
	EarliestCreatedAt string         `json:"earliest_created_at"`
	LatestCreatedAt   string         `json:"latest_created_at"`
	SampleRecordIDs   []int64        `json:"sample_record_ids"`
}

type inventoryReport struct {
	Mode         string         `json:"mode"`
	ReadOnly     bool           `json:"read_only"`
	GeneratedAt  string         `json:"generated_at"`
	TenantFilter string         `json:"tenant_filter,omitempty"`
	Scanned      scanCounts     `json:"scanned"`
	Categories   categoryCounts `json:"categories"`
	Tenants      []tenantStat   `json:"tenants"`
	Groups       []groupStat    `json:"groups"`
}

type groupRow struct {
	TenantID          string
	ProjectID         string
	FlaggedRecords    int64
	Categories        categoryCounts
	EarliestCreatedAt string
	LatestCreatedAt   string
	SampleRecordIDs   []int64
}

func main() {
	opts, err := parseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: %v\n", err)
		os.Exit(2)
	}
	dsn, err := resolveDSN(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: %v\n", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: 打开数据库失败: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: 关闭数据库连接失败: %v\n", closeErr)
		}
	}()
	// 只读会话的 SET 只作用于单条连接，因此把连接池收敛为 1，确保后续 SELECT
	// 都跑在同一个只读会话里；盘点本身是串行只读，不需要并发连接。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: 数据库连通性检查失败: %v\n", err)
		os.Exit(1)
	}
	if err := enforceReadOnlySession(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: %v\n", err)
		os.Exit(1)
	}
	report, err := collect(ctx, db, opts.tenant)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: 盘点失败: %v\n", err)
		os.Exit(1)
	}
	if opts.format == "json" {
		err = renderJSON(os.Stdout, report)
	} else {
		err = renderTable(os.Stdout, report)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence-ledger-inventory: 输出失败: %v\n", err)
		os.Exit(1)
	}
}

// parseArgs 只解析盘点参数，不连接数据库，便于纯函数测试。
func parseArgs(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("evidence-ledger-inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", defaultFormat, "输出格式：table 或 json")
	tenant := fs.String("tenant", "", "只盘点指定租户；缺省盘点全部租户（只读、受租户过滤）")
	timeout := fs.Duration("timeout", defaultTimeout, "盘点查询超时时间（只读 SELECT）")
	apply := fs.Bool("apply", false, "不实现：回填需单独立项审批")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("不接受位置参数：%s", strings.Join(fs.Args(), " "))
	}
	if *apply {
		return options{}, errors.New(applyRefusal)
	}
	formatValue := strings.ToLower(strings.TrimSpace(*format))
	if formatValue != "table" && formatValue != "json" {
		return options{}, fmt.Errorf("--format 仅支持 table 或 json，收到 %q", *format)
	}
	if *timeout <= 0 {
		return options{}, fmt.Errorf("--timeout 必须大于 0，收到 %s", timeout.String())
	}
	tenantValue := strings.TrimSpace(*tenant)
	if err := validateTenantFilter(tenantValue); err != nil {
		return options{}, err
	}
	return options{format: formatValue, tenant: tenantValue, timeout: *timeout}, nil
}

// validateTenantFilter 只做输入健全性检查；SQL 仍使用占位符，不做字符串拼接。
func validateTenantFilter(tenant string) error {
	if tenant == "" {
		return nil
	}
	if len(tenant) > 64 {
		return fmt.Errorf("--tenant 长度不能超过 64，收到 %d", len(tenant))
	}
	if strings.ContainsFunc(tenant, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return errors.New("--tenant 不能包含空白或控制字符")
	}
	return nil
}

// resolveDSN 复用既有 cmd 的 MYSQL_DSN 环境变量，避免另建一套连接配置。
func resolveDSN(getenv func(string) string) (string, error) {
	dsn := strings.TrimSpace(getenv("MYSQL_DSN"))
	if dsn == "" {
		return "", errors.New("MYSQL_DSN 未配置：本命令与 cmd/migrate 使用同一环境变量")
	}
	return dsn, nil
}

// enforceReadOnlySession 把当前会话置为只读：即使未来有人误加写语句，
// 数据库也会拒绝，而不是把盘点命令变成隐式写库工具。
func enforceReadOnlySession(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
		return fmt.Errorf("设置只读会话失败（拒绝在非只读会话盘点）: %w", err)
	}
	return nil
}

// buildTenantScanQuery 统计每个 tenant 的证据台账记录总数，作为盘点分母。
func buildTenantScanQuery(tenantFilter string) (string, []any) {
	query := "SELECT tenant_id, COUNT(*) AS records FROM pm_evidence_file"
	args := make([]any, 0, 1)
	if tenantFilter != "" {
		query += " WHERE tenant_id = ?"
		args = append(args, tenantFilter)
	}
	query += " GROUP BY tenant_id ORDER BY tenant_id ASC"
	return query, args
}

// buildInventoryQuery 只读统计异常记录。每条记录可同时命中多个口径：
// flagged_records 是按记录去重后的数量，categories 各项可重叠。
func buildInventoryQuery(tenantFilter string) (string, []any) {
	query := `SELECT
  e.tenant_id,
  COALESCE(si.project_id, '') AS project_id,
  COUNT(*) AS flagged_records,
  COUNT(CASE WHEN TRIM(e.service_item_id) = '' THEN 1 END) AS missing_service_item,
  COUNT(CASE WHEN TRIM(e.service_item_id) <> '' AND si.id IS NULL THEN 1 END) AS unknown_service_item,
  COUNT(CASE WHEN TRIM(e.file_id) = '' THEN 1 END) AS empty_file_id,
  COUNT(CASE WHEN e.evidence_kind NOT IN ('FIELD','DEVIATION','REPORT') THEN 1 END) AS unexpected_kind,
  DATE_FORMAT(MIN(e.created_at), '%Y-%m-%dT%H:%i:%s.%fZ') AS earliest_created_at,
  DATE_FORMAT(MAX(e.created_at), '%Y-%m-%dT%H:%i:%s.%fZ') AS latest_created_at,
  SUBSTRING_INDEX(GROUP_CONCAT(e.id ORDER BY e.id SEPARATOR ','), ',', 5) AS sample_record_ids
FROM pm_evidence_file AS e
LEFT JOIN pm_service_item AS si
  ON si.tenant_id = e.tenant_id AND si.id = e.service_item_id
WHERE (
    TRIM(e.service_item_id) = ''
    OR (TRIM(e.service_item_id) <> '' AND si.id IS NULL)
    OR TRIM(e.file_id) = ''
    OR e.evidence_kind NOT IN ('FIELD','DEVIATION','REPORT')
  )`
	args := make([]any, 0, 1)
	if tenantFilter != "" {
		query += " AND e.tenant_id = ?"
		args = append(args, tenantFilter)
	}
	query += `
GROUP BY e.tenant_id, COALESCE(si.project_id, '')
ORDER BY e.tenant_id ASC, project_id ASC`
	return query, args
}

func loadTenantScan(ctx context.Context, db *sql.DB, tenantFilter string) (map[string]int64, error) {
	query, args := buildTenantScanQuery(tenantFilter)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("统计 pm_evidence_file 总量失败: %w", err)
	}
	defer rows.Close()
	scanned := make(map[string]int64)
	for rows.Next() {
		var tenantID string
		var records int64
		if err := rows.Scan(&tenantID, &records); err != nil {
			return nil, fmt.Errorf("读取 pm_evidence_file 总量失败: %w", err)
		}
		scanned[tenantID] = records
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取 pm_evidence_file 总量失败: %w", err)
	}
	return scanned, nil
}

func loadGroups(ctx context.Context, db *sql.DB, tenantFilter string) ([]groupRow, error) {
	query, args := buildInventoryQuery(tenantFilter)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("统计证据台账绑定异常失败: %w", err)
	}
	defer rows.Close()
	groups := make([]groupRow, 0, 16)
	for rows.Next() {
		var (
			row       groupRow
			earliest  sql.NullString
			latest    sql.NullString
			sampleRaw sql.NullString
		)
		if err := rows.Scan(
			&row.TenantID,
			&row.ProjectID,
			&row.FlaggedRecords,
			&row.Categories.MissingServiceItem,
			&row.Categories.UnknownServiceItem,
			&row.Categories.EmptyFileID,
			&row.Categories.UnexpectedKind,
			&earliest,
			&latest,
			&sampleRaw,
		); err != nil {
			return nil, fmt.Errorf("读取证据台账绑定异常失败: %w", err)
		}
		row.EarliestCreatedAt = earliest.String
		row.LatestCreatedAt = latest.String
		sampleIDs, err := parseSampleIDs(sampleRaw.String)
		if err != nil {
			return nil, fmt.Errorf("解析 tenant=%s project=%s 的样本记录 ID 失败: %w", row.TenantID, row.ProjectID, err)
		}
		row.SampleRecordIDs = sampleIDs
		groups = append(groups, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取证据台账绑定异常失败: %w", err)
	}
	return groups, nil
}

// parseSampleIDs 解析 GROUP_CONCAT 产出的记录 ID 列表；SQL 已截断到 5 个，
// 这里再做一次上限保护，避免数据库端配置变化导致输出膨胀。
func parseSampleIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			return nil, errors.New("样本记录 ID 列表包含空项")
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("样本记录 ID %q 不是整数: %w", value, err)
		}
		if id <= 0 {
			return nil, fmt.Errorf("样本记录 ID %d 必须为正数", id)
		}
		ids = append(ids, id)
		if len(ids) == sampleIDLimit {
			break
		}
	}
	return ids, nil
}

// collect 只做只读查询与内存聚合，任何查询错误都向上返回，不吞异常、不降级为空结果。
func collect(ctx context.Context, db *sql.DB, tenantFilter string) (inventoryReport, error) {
	scannedByTenant, err := loadTenantScan(ctx, db, tenantFilter)
	if err != nil {
		return inventoryReport{}, err
	}
	rows, err := loadGroups(ctx, db, tenantFilter)
	if err != nil {
		return inventoryReport{}, err
	}
	report := inventoryReport{
		Mode:         modeDryRun,
		ReadOnly:     true,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		TenantFilter: tenantFilter,
	}
	for tenantID, records := range scannedByTenant {
		report.Scanned.Total += records
		report.Tenants = append(report.Tenants, tenantStat{TenantID: tenantID, ScannedRecords: records})
	}
	tenants := make(map[string]*tenantStat, len(report.Tenants))
	for index := range report.Tenants {
		tenants[report.Tenants[index].TenantID] = &report.Tenants[index]
	}
	tenantProjects := make(map[string]map[string]struct{}, len(report.Tenants))
	for _, row := range rows {
		tenant := tenants[row.TenantID]
		if tenant == nil {
			return inventoryReport{}, fmt.Errorf("盘点租户汇总缺少 tenant=%s：租户汇总与分组结果不一致", row.TenantID)
		}
		tenant.FlaggedRecords += row.FlaggedRecords
		tenant.Categories = tenant.Categories.add(row.Categories)
		if tenantProjects[row.TenantID] == nil {
			tenantProjects[row.TenantID] = make(map[string]struct{})
		}
		tenantProjects[row.TenantID][row.ProjectID] = struct{}{}
		report.Scanned.Flagged += row.FlaggedRecords
		report.Categories = report.Categories.add(row.Categories)
		report.Groups = append(report.Groups, groupStat{
			TenantID:          row.TenantID,
			ProjectID:         row.ProjectID,
			FlaggedRecords:    row.FlaggedRecords,
			Categories:        row.Categories,
			EarliestCreatedAt: row.EarliestCreatedAt,
			LatestCreatedAt:   row.LatestCreatedAt,
			SampleRecordIDs:   row.SampleRecordIDs,
		})
	}
	for index := range report.Tenants {
		report.Tenants[index].Projects = len(tenantProjects[report.Tenants[index].TenantID])
	}
	sort.Slice(report.Tenants, func(i, j int) bool {
		return report.Tenants[i].TenantID < report.Tenants[j].TenantID
	})
	if report.Groups == nil {
		report.Groups = []groupStat{}
	}
	if report.Tenants == nil {
		report.Tenants = []tenantStat{}
	}
	return report, nil
}

func renderJSON(w io.Writer, report inventoryReport) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func renderTable(w io.Writer, report inventoryReport) error {
	if _, err := fmt.Fprintf(w, "mode=%s read_only=%t generated_at=%s\n", report.Mode, report.ReadOnly, report.GeneratedAt); err != nil {
		return err
	}
	if report.TenantFilter != "" {
		if _, err := fmt.Fprintf(w, "tenant_filter=%s\n", report.TenantFilter); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "scanned_total=%d flagged_records=%d\n", report.Scanned.Total, report.Scanned.Flagged); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		w,
		"categories: missing_service_item=%d unknown_service_item=%d empty_file_id=%d unexpected_kind=%d\n\n",
		report.Categories.MissingServiceItem,
		report.Categories.UnknownServiceItem,
		report.Categories.EmptyFileID,
		report.Categories.UnexpectedKind,
	); err != nil {
		return err
	}
	tenantTable := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tenantTable, "TENANT\tSCANNED\tFLAGGED\tPROJECTS\tMISSING_SI\tUNKNOWN_SI\tEMPTY_FILE_ID\tUNEXPECTED_KIND"); err != nil {
		return err
	}
	for _, tenant := range report.Tenants {
		if _, err := fmt.Fprintf(
			tenantTable,
			"%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
			tenant.TenantID,
			tenant.ScannedRecords,
			tenant.FlaggedRecords,
			tenant.Projects,
			tenant.Categories.MissingServiceItem,
			tenant.Categories.UnknownServiceItem,
			tenant.Categories.EmptyFileID,
			tenant.Categories.UnexpectedKind,
		); err != nil {
			return err
		}
	}
	if err := tenantTable.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	groupTable := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(groupTable, "TENANT\tPROJECT\tFLAGGED\tMISSING_SI\tUNKNOWN_SI\tEMPTY_FILE_ID\tUNEXPECTED_KIND\tEARLIEST\tLATEST\tSAMPLE_IDS"); err != nil {
		return err
	}
	if len(report.Groups) == 0 {
		if _, err := fmt.Fprintln(groupTable, "-\t-\t0\t0\t0\t0\t0\t-\t-\t-"); err != nil {
			return err
		}
	}
	for _, group := range report.Groups {
		project := group.ProjectID
		if project == "" {
			project = "-"
		}
		if _, err := fmt.Fprintf(
			groupTable,
			"%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n",
			group.TenantID,
			project,
			group.FlaggedRecords,
			group.Categories.MissingServiceItem,
			group.Categories.UnknownServiceItem,
			group.Categories.EmptyFileID,
			group.Categories.UnexpectedKind,
			group.EarliestCreatedAt,
			group.LatestCreatedAt,
			formatSampleIDs(group.SampleRecordIDs),
		); err != nil {
			return err
		}
	}
	return groupTable.Flush()
}

func formatSampleIDs(ids []int64) string {
	if len(ids) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}
