package domain

import "time"

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Customer  string    `json:"customer"`
	Contract  string    `json:"contract"`
	Services  int       `json:"services"`
	Category  string    `json:"category"`
	Team      string    `json:"team"`
	Manager   string    `json:"manager"`
	Health    string    `json:"health"`
	Status    string    `json:"status"`
	Progress  int       `json:"progress"`
	Due       string    `json:"due"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ServiceItem struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Batch       string `json:"batch"`
	Site        string `json:"site"`
	Category    string `json:"category"`
	Requirement string `json:"requirement"`
	System      string `json:"system"`
	Special     string `json:"special"`
	Status      string `json:"status"`
}

type Rule struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	Trigger string `json:"trigger"`
	Enabled bool   `json:"enabled"`
	Updated string `json:"updated"`
}

type Snapshot struct {
	Projects     []Project     `json:"projects"`
	ServiceItems []ServiceItem `json:"service_items"`
	Rules        []Rule        `json:"rules"`
}

type Dashboard struct {
	ProjectCount     int            `json:"project_count"`
	InFlightProjects int            `json:"in_flight_projects"`
	RiskProjects     int            `json:"risk_projects"`
	ServiceItems     int            `json:"service_items"`
	StatusCounts     map[string]int `json:"status_counts"`
}
