package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListSites 返回租户的站点台账，按编码排序。
func (r *Repository) ListSites(ctx context.Context, tenantID, status string) ([]domain.Site, error) {
	query := r.db.WithContext(ctx).Model(&siteRecord{}).Where("tenant_id = ?", tenantID)
	if trimmed := strings.TrimSpace(status); trimmed != "" {
		query = query.Where("status = ?", trimmed)
	}
	var records []siteRecord
	if err := query.Order("site_code ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.Site, 0, len(records))
	for _, record := range records {
		items = append(items, siteFromRecord(record))
	}
	return items, nil
}

// UpsertSite 按 (tenant_id, site_code) 幂等写入站点档案。
// 坐标未采集时写入 NULL，而不是 0——否则地图上会多出一个几内亚湾的点。
func (r *Repository) UpsertSite(ctx context.Context, item domain.Site) (domain.Site, error) {
	if strings.TrimSpace(item.ID) == "" {
		item.ID = ulid.Make().String()
	}
	now := time.Now().UTC()
	record := siteRecord{
		ID: item.ID, TenantID: item.TenantID, SiteCode: strings.TrimSpace(item.SiteCode),
		Name: strings.TrimSpace(item.Name), Address: strings.TrimSpace(item.Address),
		Status: firstValue(item.Status, "ACTIVE"), Notes: strings.TrimSpace(item.Notes),
		CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedAt,
	}
	if item.HasCoordinates || item.Latitude != 0 || item.Longitude != 0 {
		latitude, longitude := item.Latitude, item.Longitude
		record.Latitude, record.Longitude = &latitude, &longitude
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "site_code"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "address", "latitude", "longitude", "status", "notes", "updated_at", "updated_by"}),
	}).Create(&record).Error
	if err != nil {
		return domain.Site{}, err
	}
	// 回读以拿到自增冲突时的既有主键，保证返回值与库内一致。
	var stored siteRecord
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND site_code = ?", item.TenantID, record.SiteCode).Take(&stored).Error; err != nil {
		return domain.Site{}, err
	}
	return siteFromRecord(stored), nil
}

// DeleteSite 停用站点：保留行以维持历史服务项的可追溯性，不做物理删除。
func (r *Repository) DeleteSite(ctx context.Context, tenantID, siteCode string) error {
	result := r.db.WithContext(ctx).Model(&siteRecord{}).
		Where("tenant_id = ? AND site_code = ?", tenantID, strings.TrimSpace(siteCode)).
		Updates(map[string]any{"status": "DISABLED", "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return application.ErrNotFound
	}
	return nil
}

// FindSiteByCode 供服务项关联站点时校验编码存在。
func (r *Repository) FindSiteByCode(ctx context.Context, tenantID, siteCode string) (domain.Site, error) {
	var record siteRecord
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND site_code = ?", tenantID, strings.TrimSpace(siteCode)).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Site{}, application.ErrNotFound
	}
	if err != nil {
		return domain.Site{}, err
	}
	return siteFromRecord(record), nil
}

func siteFromRecord(record siteRecord) domain.Site {
	item := domain.Site{
		ID: record.ID, SiteCode: record.SiteCode, Name: record.Name, Address: record.Address,
		Status: record.Status, Notes: record.Notes, UpdatedAt: record.UpdatedAt.Format(time.RFC3339),
	}
	if record.Latitude != nil && record.Longitude != nil {
		item.Latitude, item.Longitude, item.HasCoordinates = *record.Latitude, *record.Longitude, true
	}
	return item
}
