package store

import (
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

func Seed() domain.Snapshot {
	now := time.Date(2026, 7, 31, 5, 0, 0, 0, time.UTC)
	projects := []domain.Project{
		{ID: "PJ-2026-0826", Name: "某省政务云安全测评项目", Customer: "某省政务云", Contract: "HT-2026-0411", Services: 22, Category: "等保测评 / 商用密码", Team: "测评一组", Manager: "张磊", Health: "正常", Status: "待分配", Progress: 36, Due: "2026-08-15", CreatedAt: now, UpdatedAt: now},
		{ID: "PJ-2026-0823", Name: "某银行股份等保测评项目", Customer: "某银行股份", Contract: "HT-2026-0408", Services: 14, Category: "等保测评", Team: "测评一组", Manager: "张磊", Health: "正常", Status: "实施中", Progress: 68, Due: "2026-07-22", CreatedAt: now, UpdatedAt: now},
		{ID: "PJ-2026-0817", Name: "某证券交易所安全测试项目", Customer: "某证券交易所", Contract: "HT-2026-0402", Services: 9, Category: "等保测评 / 渗透测试", Team: "测评二组", Manager: "李娜", Health: "风险", Status: "异常处理中", Progress: 55, Due: "超期 3 天", CreatedAt: now, UpdatedAt: now},
		{ID: "PJ-2026-0825", Name: "某三甲医院安全服务项目", Customer: "某三甲医院", Contract: "HT-2026-0410", Services: 7, Category: "渗透测试 / 应急响应", Team: "渗透测试组", Manager: "王明", Health: "关注", Status: "报告编制", Progress: 82, Due: "2026-07-25", CreatedAt: now, UpdatedAt: now},
		{ID: "PJ-2026-0831", Name: "某航空公司软件测试项目", Customer: "某航空公司", Contract: "HT-2026-0414", Services: 12, Category: "源代码审计 / 软件测试", Team: "测评二组", Manager: "李娜", Health: "正常", Status: "待实施", Progress: 24, Due: "2026-08-30", CreatedAt: now, UpdatedAt: now},
		{ID: "PJ-2026-0833", Name: "某市公积金等保测评项目", Customer: "某市公积金", Contract: "HT-2026-0415", Services: 8, Category: "等保测评", Team: "未分配", Manager: "—", Health: "待确认", Status: "待拆解确认", Progress: 8, Due: "—", CreatedAt: now, UpdatedAt: now},
	}
	items := []domain.ServiceItem{
		{ID: "SI-0833-01", ProjectID: "PJ-2026-0833", Batch: "第一批次", Site: "总部数据中心", Category: "等保测评（三级）", Requirement: "核心业务系统 8 套", System: "ISO 27001", Special: "否", Status: "待确认"},
		{ID: "SI-0833-02", ProjectID: "PJ-2026-0833", Batch: "第一批次", Site: "总部数据中心", Category: "商用密码应用安全性评估", Requirement: "密码应用方案与密评", System: "—", Special: "否", Status: "待确认"},
		{ID: "SI-0833-03", ProjectID: "PJ-2026-0833", Batch: "第一批次", Site: "灾备中心", Category: "渗透测试", Requirement: "互联网暴露面黑盒测试", System: "ISO 27001", Special: "是", Status: "待复核"},
		{ID: "SI-0833-04", ProjectID: "PJ-2026-0833", Batch: "第二批次", Site: "办公区", Category: "等保测评（二级）", Requirement: "办公内网与终端安全", System: "—", Special: "否", Status: "待确认"},
	}
	rules := []domain.Rule{
		{ID: 1, Kind: "split-rules", Name: "按批次 + 检测类别拆解", Scope: "等保测评、商用密码", Trigger: "合同生效", Enabled: true, Updated: "2026-07-16 14:20"},
		{ID: 2, Kind: "split-rules", Name: "特殊方法独立成项", Scope: "全部检测类别", Trigger: "服务清单含特殊方法", Enabled: true, Updated: "2026-07-12 09:42"},
		{ID: 3, Kind: "warning-rules", Name: "跨地域实施冲突预警", Scope: "项目经理、实施工程师", Trigger: "排期重叠且跨城市", Enabled: true, Updated: "2026-07-08 17:10"},
		{ID: 4, Kind: "warning-rules", Name: "资质临期强提醒", Scope: "人员资质", Trigger: "有效期不足 90 天", Enabled: false, Updated: "2026-06-28 11:05"},
	}
	return domain.Snapshot{Projects: projects, ServiceItems: items, Rules: rules}
}
