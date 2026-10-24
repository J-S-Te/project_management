package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"time"
	"unicode/utf8"
)

type ProjectProjection struct {
	Type              string    `json:"type"`
	TenantID          string    `json:"tenant_id"`
	SnapshotID        string    `json:"snapshot_id"`
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Customer          string    `json:"customer"`
	CustomerID        string    `json:"customer_id"`
	Contract          string    `json:"contract"`
	ContractID        string    `json:"contract_id"`
	Category          string    `json:"category"`
	Team              string    `json:"team"`
	Manager           string    `json:"manager"`
	ManagerIdentityID string    `json:"manager_identity_id"`
	OwnerOrgID        string    `json:"owner_org_id"`
	OwnerIdentityID   string    `json:"owner_identity_id"`
	Status            string    `json:"status"`
	Progress          int       `json:"progress"`
	Risk              bool      `json:"risk"`
	InFlight          bool      `json:"in_flight"`
	ServiceItems      int       `json:"service_items"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	SourceUpdatedAt   time.Time `json:"source_updated_at"`
}

type ProjectSnapshotHeader struct {
	Type             string    `json:"type"`
	SchemaVersion    int       `json:"schema_version"`
	TenantID         string    `json:"tenant_id"`
	SnapshotID       string    `json:"snapshot_id"`
	SourceSnapshotAt time.Time `json:"source_snapshot_at"`
}

type ProjectSnapshotSummary struct {
	Type           string `json:"type"`
	SchemaVersion  int    `json:"schema_version"`
	TenantID       string `json:"tenant_id"`
	SnapshotID     string `json:"snapshot_id"`
	Total          int    `json:"total"`
	ChecksumSHA256 string `json:"checksum_sha256"`
}

// ProjectSnapshotItem includes archive metadata so only active work participates in metrics.
type ProjectSnapshotItem struct {
	Status, ReportStatus string
	UpdatedAt            time.Time
	ArchivedAt           *time.Time
}

// NewProjectProjection reuses the authoritative lifecycle functions on one active set.
func NewProjectProjection(project Project, snapshotID string, items []ProjectSnapshotItem, sourceUpdatedAt time.Time) ProjectProjection {
	active := make([]ProjectStatusItem, 0, len(items))
	latest := project.UpdatedAt
	if project.CreatedAt.After(latest) {
		latest = project.CreatedAt
	}
	if sourceUpdatedAt.After(latest) {
		latest = sourceUpdatedAt
	}
	for _, item := range items {
		if item.UpdatedAt.After(latest) {
			latest = item.UpdatedAt
		}
		if item.ArchivedAt != nil {
			if item.ArchivedAt.After(latest) {
				latest = *item.ArchivedAt
			}
			continue
		}
		active = append(active, ProjectStatusItem{Status: item.Status, ReportStatus: item.ReportStatus})
	}
	status := DeriveProjectStatus(active, project.SupplementStatus, project.Status)
	return ProjectProjection{
		Type: "project", TenantID: project.TenantID, SnapshotID: snapshotID, ID: project.ID, Name: project.Name,
		Customer: project.Customer, CustomerID: project.CustomerID, Contract: project.Contract, ContractID: project.ContractID,
		Category: project.Category, Team: project.Team, Manager: project.Manager, ManagerIdentityID: project.ManagerIdentityID,
		OwnerOrgID: project.OwnerOrgID, OwnerIdentityID: project.OwnerIdentityID, Status: status,
		Progress: DeriveProjectProgress(active), Risk: IsRiskProject(status, active), InFlight: status != ProjectStatusCompleted,
		ServiceItems: len(active), CreatedAt: project.CreatedAt.UTC(), UpdatedAt: project.UpdatedAt.UTC(), SourceUpdatedAt: latest.UTC(),
	}
}

// ProjectSnapshotLine is shared by byte accounting, SHA and transport. Reject invalid
// source strings before encoding/json can silently substitute replacement characters.
func ProjectSnapshotLine(value any) ([]byte, error) {
	switch value.(type) {
	case ProjectProjection, ProjectSnapshotHeader, ProjectSnapshotSummary:
	default:
		return nil, errors.New("unsupported project snapshot record")
	}
	fields := reflect.ValueOf(value)
	for i := 0; i < fields.NumField(); i++ {
		f := fields.Field(i)
		if f.Kind() == reflect.String && !utf8.ValidString(f.String()) {
			return nil, errors.New("invalid project snapshot UTF-8")
		}
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
