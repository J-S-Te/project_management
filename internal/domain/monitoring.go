package domain

import "time"

// ProjectMonitoringQuery 是服务端定义的在途控制台筛选契约。
// 全部业务筛选之后再分页，使总数和分页描述同一快照。
type ProjectMonitoringQuery struct {
	Page             int
	PageSize         int
	Keyword          string
	Status           string
	Customer         string
	Category         string
	Team             string
	ProjectManagerID string
	RiskOnly         bool
	SLAOnly          bool
	ConflictOnly     bool
	DueFrom          string
	DueTo            string
}

type MonitoringMilestone struct {
	Current string `json:"current"`
	Next    string `json:"next,omitempty"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
}

type MonitoringResources struct {
	TeamLeads         int `json:"team_leads"`
	ProjectManagers   int `json:"project_managers"`
	Engineers         int `json:"engineers"`
	Equipment         int `json:"equipment"`
	ExpiredEquipment  int `json:"expired_equipment"`
	ReleasedEquipment int `json:"released_equipment"`
	ConflictItems     int `json:"conflict_items"`
}

type MonitoringProject struct {
	Project             Project             `json:"project"`
	ServiceStatusCounts map[string]int      `json:"service_status_counts"`
	Categories          []string            `json:"categories"`
	ProjectManagerIDs   []string            `json:"project_manager_ids"`
	Milestone           MonitoringMilestone `json:"milestone"`
	Resources           MonitoringResources `json:"resources"`
	SLAItems            []SlaOverdueItem    `json:"sla_items"`
	LatestEvent         *DeliveryEvent      `json:"latest_event,omitempty"`
	PlannedEnd          string              `json:"planned_end,omitempty"`
	UpdatedAt           time.Time           `json:"updated_at"`
	ActionSection       string              `json:"action_section"`
}

type ProjectMonitoringSnapshot struct {
	Items             []MonitoringProject `json:"items"`
	RecentEvents      []DeliveryEvent     `json:"recent_events"`
	StatusCounts      map[string]int      `json:"status_counts"`
	Categories        []string            `json:"categories"`
	Teams             []string            `json:"teams"`
	ProjectManagerIDs []string            `json:"project_manager_ids"`
	Total             int                 `json:"total"`
	Page              int                 `json:"page"`
	PageSize          int                 `json:"page_size"`
	ServerTime        time.Time           `json:"server_time"`
	SnapshotVersion   string              `json:"snapshot_version"`
}

// ProjectMonitoringPageData 是单个数据库页的仓储到应用投影。
// 仅请求页的项目携带服务、SLA 和事件详情；总数及分面由
// MySQL 从授权范围内完整筛选结果计算。
type ProjectMonitoringPageData struct {
	Projects          []Project
	ServiceItems      []ServiceItem
	SLACandidates     []SlaOverdueItem
	Events            []DeliveryEvent
	StatusCounts      map[string]int
	Categories        []string
	Teams             []string
	ProjectManagerIDs []string
	Total             int
	LatestUpdatedAt   time.Time
}
