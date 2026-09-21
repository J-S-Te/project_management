package httpapi

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
)

func detectionCategoriesFromCSV(content []byte) ([]domain.DetectionCategory, []string) {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, []string{"CSV 格式不合法"}
	}
	start := 0
	if len(records) > 0 && len(records[0]) > 0 && strings.TrimSpace(records[0][0]) == "检测类别" {
		start = 1
	}
	items := make([]domain.DetectionCategory, 0, len(records)-start)
	errs := make([]string, 0)
	value := func(row []string, index int) string {
		if index < len(row) {
			return strings.TrimSpace(row[index])
		}
		return ""
	}
	for index := start; index < len(records); index++ {
		row := records[index]
		category := value(row, 0)
		if category == "" {
			if len(row) > 0 {
				errs = append(errs, fmt.Sprintf("CSV 第 %d 行缺少检测类别", index+1))
			}
			continue
		}
		special := map[string]string{"": "NO", "否": "NO", "NO": "NO", "可标记": "MARKABLE", "MARKABLE": "MARKABLE", "必为特殊方法": "REQUIRED", "REQUIRED": "REQUIRED"}[strings.ToUpper(value(row, 4))]
		if special == "" {
			errs = append(errs, fmt.Sprintf("CSV 第 %d 行特殊方法不合法", index+1))
			continue
		}
		status := value(row, 5)
		items = append(items, domain.DetectionCategory{Category: category, SystemStandard: value(row, 1), RequiredQualifications: value(row, 2), RequiredCodes: value(row, 3), SpecialMethod: special, Enabled: status == "" || status == "启用" || strings.EqualFold(status, "ACTIVE") || strings.EqualFold(status, "TRUE")})
	}
	return items, errs
}
