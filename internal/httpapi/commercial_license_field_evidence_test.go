package httpapi_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	core "github.com/J-S-Te/license-core"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

// expiredLicenseGate 模拟商业授权到期后的只读期：只拒绝 MUTATE_BUSINESS。
type expiredLicenseGate struct{}

func (expiredLicenseGate) Check(_ context.Context, operation core.Operation) error {
	if operation == core.MUTATE_BUSINESS {
		return errors.New("expired")
	}
	return nil
}

// PRJ-01：GET /service-items/:id/field-evidence 是只读的缺项判定回读。许可到期只读期
// 必须仍返回 200；一旦漏出只读白名单（默认 MUTATE_BUSINESS）会 403
// COMMERCIAL_LICENSE_DENIED，新前端 fail-closed 因而阻断"现场完成"。
func TestServiceItemFieldEvidenceReadableInExpiredLicenseReadOnlyPeriod(t *testing.T) {
	repository := &repo{
		items:  []domain.ServiceItem{{ID: "SI-1", ProjectID: "PJ-FIELD-1", Status: "实施中"}},
		events: []domain.DeliveryEvent{fieldEvidenceEvent("SI-1", application.EventFieldCheckedIn, nil)},
	}
	principal := fieldEvidencePrincipal("reader-1",
		map[string]bool{"project.read": true, "project.field.complete": true},
		[]platform.DataScope{{RoleCode: "project-manager", ScopeType: "APPLICATION"}})
	router := httpapi.NewRouter(
		&application.Service{Repo: repository, Contracts: approvedContractVerifier{}},
		identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpapi.RouterOptions{CommercialLicense: expiredLicenseGate{}},
	)

	before := len(repository.events)
	read := perform(router, http.MethodGet, "/api/v1/service-items/SI-1/field-evidence", "")
	if read.Code != http.StatusOK {
		t.Fatalf("field-evidence in read-only period status=%d body=%s", read.Code, read.Body.String())
	}
	if strings.Contains(read.Body.String(), "COMMERCIAL_LICENSE_DENIED") {
		t.Fatalf("field-evidence classified as mutate: %s", read.Body.String())
	}
	if len(repository.events) != before {
		t.Fatalf("field-evidence must not write events: %+v", repository.events)
	}

	// 同一只读期内写接口必须继续 403：本次只补分类，没有放宽其它许可门禁。
	write := perform(router, http.MethodPost, "/api/v1/service-items/SI-1/field-complete", "{}")
	if write.Code != http.StatusForbidden || !strings.Contains(write.Body.String(), "COMMERCIAL_LICENSE_DENIED") {
		t.Fatalf("field-complete in read-only period status=%d body=%s", write.Code, write.Body.String())
	}
}
