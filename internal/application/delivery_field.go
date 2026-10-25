package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"io"
	"strings"
	"time"
)

func (s *Service) UploadEvidence(ctx context.Context, p platform.Principal, itemID, kind, requestID, fileName, mimeType string, content io.Reader) (domain.ReportArtifactInput, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	permission := "project.field.execute"
	if kind == "DEVIATION" {
		permission = "project.deviation.report"
	}
	if kind == "REPORT" {
		permission = "project.report.prepare"
	}
	if kind != "FIELD" && kind != "DEVIATION" && kind != "REPORT" {
		return domain.ReportArtifactInput{}, ValidationError("证据类型不正确")
	}
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if mimeType != "application/pdf" && mimeType != "image/png" && mimeType != "image/jpeg" {
		return domain.ReportArtifactInput{}, ValidationError("证据文件仅支持 PDF、PNG 或 JPEG")
	}
	if err := s.authorizeServiceItem(ctx, p, permission, itemID); err != nil {
		return domain.ReportArtifactInput{}, err
	}
	if s.EvidenceFiles == nil {
		return domain.ReportArtifactInput{}, errors.New("统一文件网关未配置")
	}
	artifact, err := s.EvidenceFiles.UploadEvidence(ctx, requestID, itemID, kind, fileName, mimeType, content)
	if err != nil {
		return domain.ReportArtifactInput{}, err
	}
	if err = normalizeFileEvidence(&artifact, "证据"); err != nil {
		return domain.ReportArtifactInput{}, err
	}
	return artifact, nil
}

// reportPhasePermission 返回推进到目标报告阶段所需的权限码。
// 归档（ARCHIVED）会把项目推向"已完成"，属于独立治理动作；编制/审核/签发属于报告编制。

func (s *Service) StartPreparation(ctx context.Context, p platform.Principal, itemID string, input domain.PreparationInput) error {
	filter, err := authorizeProjectScope(p, "project.implementation.plan")
	if err != nil {
		return err
	}
	if err := validateTravelTexts(input.Travel); err != nil {
		return err
	}
	if err := validateProjectTextLength(input.Notes, 2000, "实施准备备注"); err != nil {
		return err
	}
	for index, equipment := range input.Equipment {
		if err := validateProjectTextLength(equipment.Note, 500, fmt.Sprintf("第 %d 行设备备注", index+1)); err != nil {
			return err
		}
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.implementation.plan", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	// 设备清单在实施准备阶段确定：以计划的计划起止为占用区间边界，占用重叠直接拒绝，
	// 避免同一台设备被两个服务项在同一时间段内同时占用。
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	var projectEvents []domain.DeliveryEvent
	if strings.EqualFold(strings.TrimSpace(input.Travel.Mode), domain.TravelModeExisting) {
		projectEvents, err = repo.ListDeliveryEvents(ctx, filter, item.ProjectID)
		if err != nil {
			return err
		}
	}
	travel, err := normalizeTravelArrangement(input, item, projectEvents)
	if err != nil {
		return err
	}
	planStart, planEnd, err := planWindowOf(item)
	if err != nil {
		return err
	}
	equipment, err := s.resolvePreparationEquipment(ctx, repo, p.TenantID, itemID, input.Equipment, planStart, planEnd)
	if err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventPreparationStarted, map[string]any{
		"expected_version":  input.ExpectedVersion,
		"travel_request_id": travel.RequestID,
		"travel":            travel,
		"notes":             strings.TrimSpace(input.Notes),
		"equipment":         equipment,
	}))
}

// normalizeTravelArrangement 把旧版裸编号和新版结构化行程统一成可审计快照。
// 新建行程编号必须由服务端生成；关联已有行程必须引用当前项目真实存在的准备事件。

// normalizeTravelArrangement 把旧版裸编号和新版结构化行程统一成可审计快照。
// 新建行程编号必须由服务端生成；关联已有行程必须引用当前项目真实存在的准备事件。
func normalizeTravelArrangement(input domain.PreparationInput, item domain.ServiceItem, projectEvents []domain.DeliveryEvent) (domain.TravelArrangementInput, error) {
	travel := input.Travel
	travel.Mode = strings.ToUpper(strings.TrimSpace(travel.Mode))
	travel.ReferenceEventID = strings.TrimSpace(travel.ReferenceEventID)
	travel.RequestID = strings.TrimSpace(travel.RequestID)
	travel.NoTravelReason = strings.TrimSpace(travel.NoTravelReason)
	travel.Origin = strings.TrimSpace(travel.Origin)
	travel.Destination = strings.TrimSpace(travel.Destination)
	travel.DepartureDate = strings.TrimSpace(travel.DepartureDate)
	travel.ReturnDate = strings.TrimSpace(travel.ReturnDate)
	travel.Transport = strings.TrimSpace(travel.Transport)
	travel.AccommodationNeed = strings.TrimSpace(travel.AccommodationNeed)
	travel.TravelerIDs = normalizePersonnelIDs(travel.TravelerIDs)

	// 滚动升级期间允许旧客户端继续发送 travel_request_id；它只作为历史兼容快照，
	// 新版 UI 不再暴露这个可自由输入的字段。
	if travel.Mode == "" && strings.TrimSpace(input.TravelRequestID) != "" {
		travel.Mode = domain.TravelModeExisting
		travel.RequestID = strings.TrimSpace(input.TravelRequestID)
		return travel, nil
	}

	switch travel.Mode {
	case domain.TravelModeNoTravel:
		if travel.NoTravelReason == "" {
			return domain.TravelArrangementInput{}, ValidationError("无需行程时必须填写原因")
		}
		return domain.TravelArrangementInput{Mode: travel.Mode, NoTravelReason: travel.NoTravelReason}, nil
	case domain.TravelModeExisting:
		if travel.ReferenceEventID == "" {
			return domain.TravelArrangementInput{}, ValidationError("请选择已有行程")
		}
		for _, event := range projectEvents {
			if event.ID != travel.ReferenceEventID || event.ProjectID != item.ProjectID || event.Type != EventPreparationStarted {
				continue
			}
			requestID := payloadText(event.Payload, "travel_request_id")
			if requestID == "" {
				return domain.TravelArrangementInput{}, ValidationError("所选记录不包含有效行程")
			}
			return domain.TravelArrangementInput{Mode: travel.Mode, ReferenceEventID: event.ID, RequestID: requestID}, nil
		}
		return domain.TravelArrangementInput{}, ValidationError("所选行程不存在或不属于当前项目")
	case domain.TravelModeNew:
		if travel.Origin == "" || travel.Destination == "" {
			return domain.TravelArrangementInput{}, ValidationError("请填写出发地和目的地")
		}
		departure, departureErr := time.Parse("2006-01-02", travel.DepartureDate)
		returnDate, returnErr := time.Parse("2006-01-02", travel.ReturnDate)
		if departureErr != nil || returnErr != nil || returnDate.Before(departure) {
			return domain.TravelArrangementInput{}, ValidationError("行程日期不完整或返回日期早于出发日期")
		}
		if len(travel.TravelerIDs) == 0 {
			return domain.TravelArrangementInput{}, ValidationError("请至少选择一名出行人员")
		}
		assigned := map[string]struct{}{}
		for _, id := range append([]string{item.TeamLeadID, item.ProjectManagerID}, item.EngineerIDs...) {
			if id = strings.TrimSpace(id); id != "" {
				assigned[id] = struct{}{}
			}
		}
		for _, id := range travel.TravelerIDs {
			if _, ok := assigned[id]; !ok {
				return domain.TravelArrangementInput{}, ValidationError("出行人员必须来自当前服务项已分配人员")
			}
		}
		travel.RequestID = "TRIP-" + time.Now().UTC().Format("20060102") + "-" + strings.ToUpper(ulid.Make().String()[20:])
		travel.ReferenceEventID = ""
		travel.NoTravelReason = ""
		return travel, nil
	default:
		return domain.TravelArrangementInput{}, ValidationError("请选择有效的行程安排方式")
	}
}

// RevokeImplementationPlan 仅在尚未开始实施准备时允许撤销计划；责任分配和能力结论仍保留，
// 以便项目经理修订排期后重新发布。计划快照由撤销事件保留，不以删除审计证据实现回退。

// RevokePreparation 在尚未提交现场记录时释放设备预约并回到待实施。设备使用历史在事件中保留，
// 清空的是活动计划行，因此设备可立即被其他项目重新预约。
func (s *Service) RevokePreparation(ctx context.Context, p platform.Principal, itemID string, input domain.PhaseRevokeInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.implementation.revoke", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.implementation.revoke", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	if strings.TrimSpace(input.Reason) == "" {
		return ValidationError("请填写撤销实施准备的原因")
	}
	filter, err := authorizeProjectScope(p, "project.implementation.revoke")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Status != "实施准备中" {
		return PreconditionError("仅可在未提交现场记录前撤销实施准备")
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventPreparationRevoked, map[string]any{"reason": strings.TrimSpace(input.Reason), "expected_version": input.ExpectedVersion}))
}

// windowLabel 用人和日期描述设备行的占用区间，让冲突提示可直接照着核对。
func windowLabel(row domain.PlanResource, planStart, planEnd time.Time) string {
	if row.WindowStart == "" {
		return fmt.Sprintf("%s ~ %s（全程）", planStart.Format("2006-01-02"), planEnd.Format("2006-01-02"))
	}
	return row.WindowStart + " ~ " + row.WindowEnd
}

func (s *Service) SubmitFieldRecord(ctx context.Context, p platform.Principal, itemID string, input domain.FieldRecordInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.execute", itemID); err != nil {
		return err
	}
	if strings.TrimSpace(input.RawData) == "" || strings.TrimSpace(input.Environment) == "" {
		return ErrValidation
	}
	if err := validateProjectTextLength(input.RawData, 2000, "现场实测数据"); err != nil {
		return err
	}
	if err := validateProjectTextLength(input.Environment, 500, "现场环境"); err != nil {
		return err
	}
	filter, err := authorizeProjectScope(p, "project.field.execute")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Status != "实施中" {
		return PreconditionError("仅可在进入实施中后提交现场记录")
	}
	if len(input.EvidenceFiles) == 0 {
		return ValidationError("现场记录必须包含至少一份文件网关证据")
	}
	if len(input.EvidenceURLs) > 0 {
		return ValidationError("现场证据必须通过统一文件网关上传")
	}
	for i := range input.EvidenceFiles {
		if err := normalizeFileEvidence(&input.EvidenceFiles[i], "现场证据"); err != nil {
			return err
		}
	}
	operationID, capturedAt, err := normalizeOfflineOperation(input.ClientOperationID, input.CapturedAt, false)
	if err != nil {
		return err
	}
	payload := map[string]any{"raw_data": input.RawData, "environment": input.Environment, "evidence_files": input.EvidenceFiles, "expected_version": input.ExpectedVersion}
	return s.applyEvent(ctx, fieldDeliveryEvent(p, itemID, EventFieldRecordSubmitted, payload, operationID, capturedAt))
}

// CheckInField 在现场作业前记录 GPS 和设备精度，支持安全重放，
// 使离线客户端恢复连接后可提交相同操作。

// CheckInField 在现场作业前记录 GPS 和设备精度，支持安全重放，
// 使离线客户端恢复连接后可提交相同操作。
func (s *Service) CheckInField(ctx context.Context, p platform.Principal, itemID string, input domain.FieldCheckInInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.execute", itemID); err != nil {
		return err
	}
	if input.Latitude < -90 || input.Latitude > 90 || input.Longitude < -180 || input.Longitude > 180 {
		return ValidationError("签到经纬度超出有效范围")
	}
	if input.AccuracyMeters <= 0 || input.AccuracyMeters > 10000 {
		return ValidationError("签到定位精度必须在 0 到 10000 米之间")
	}
	if err := validateProjectTextLength(input.DeviationReason, 1000, "签到偏离原因"); err != nil {
		return err
	}
	operationID, capturedAt, err := normalizeOfflineOperation(input.ClientOperationID, input.CapturedAt, true)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"latitude": input.Latitude, "longitude": input.Longitude, "accuracy_meters": input.AccuracyMeters,
		"deviation_reason": strings.TrimSpace(input.DeviationReason), "expected_version": input.ExpectedVersion,
	}
	return s.applyEvent(ctx, fieldDeliveryEvent(p, itemID, EventFieldCheckedIn, payload, operationID, capturedAt))
}

// CaptureFieldSignature 存储不可变签名文件及审计时间戳。

// CaptureFieldSignature 存储不可变签名文件及审计时间戳。
func (s *Service) CaptureFieldSignature(ctx context.Context, p platform.Principal, itemID string, input domain.FieldSignatureInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.execute", itemID); err != nil {
		return err
	}
	if err := normalizeFileEvidence(&input.SignatureFile, "电子签名"); err != nil {
		return err
	}
	if input.SignatureFile.MIME != "image/png" {
		return ValidationError("电子签名必须使用 PNG 格式")
	}
	operationID, capturedAt, err := normalizeOfflineOperation(input.ClientOperationID, input.CapturedAt, true)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"evidence_files": []domain.ReportArtifactInput{input.SignatureFile},
		"signature_file": input.SignatureFile, "expected_version": input.ExpectedVersion,
	}
	return s.applyEvent(ctx, fieldDeliveryEvent(p, itemID, EventFieldSignatureCaptured, payload, operationID, capturedAt))
}

func normalizeOfflineOperation(operationID, captured string, required bool) (string, time.Time, error) {
	operationID = strings.TrimSpace(operationID)
	captured = strings.TrimSpace(captured)
	if operationID == "" && captured == "" && !required {
		return "", time.Time{}, nil
	}
	if operationID == "" || captured == "" {
		return "", time.Time{}, ValidationError("离线操作编号和采集时间必须同时提供")
	}
	if len(operationID) < 8 || len(operationID) > 128 {
		return "", time.Time{}, ValidationError("离线操作编号长度必须在 8 到 128 个字符之间")
	}
	for _, r := range operationID {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._:-", r) {
			return "", time.Time{}, ValidationError("离线操作编号只能包含字母、数字、点、下划线、冒号和连字符")
		}
	}
	capturedAt, err := time.Parse(time.RFC3339, captured)
	if err != nil {
		return "", time.Time{}, ValidationError("采集时间必须是 RFC3339 格式")
	}
	if capturedAt.After(time.Now().UTC().Add(10 * time.Minute)) {
		return "", time.Time{}, ValidationError("采集时间不能晚于服务器时间 10 分钟以上")
	}
	return operationID, capturedAt.UTC(), nil
}

func fieldDeliveryEvent(p platform.Principal, itemID, typ string, payload map[string]any, operationID string, capturedAt time.Time) domain.DeliveryEvent {
	if !capturedAt.IsZero() {
		payload["captured_at"] = capturedAt.Format(time.RFC3339Nano)
	}
	return domain.DeliveryEvent{
		ID: ulid.Make().String(), TenantID: p.TenantID, ServiceItemID: itemID,
		Type: typ, ActorUserID: p.UserID, Payload: payload, CreatedAt: time.Now().UTC(),
		ClientOperationID: operationID, CapturedAt: capturedTimePointer(capturedAt),
	}
}

// StartFieldExecution 是“实施准备中”到“实施中”的显式业务动作。现场记录不再隐式推进
// 状态，避免用户尚未确认开始测评时，仅因保存一条记录就进入实施阶段。
func (s *Service) StartFieldExecution(ctx context.Context, p platform.Principal, itemID string, input domain.FieldStartInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.execute", itemID); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.field.execute", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	filter, err := authorizeProjectScope(p, "project.field.execute")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
		return err
	}
	if item.Status != "实施准备中" {
		return PreconditionError("仅可从实施准备中进入实施中")
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldStarted, map[string]any{"expected_version": input.ExpectedVersion}))
}

func (s *Service) ReportDeviation(ctx context.Context, p platform.Principal, itemID string, input domain.DeviationInput) (string, error) {
	if err := s.authorizeServiceItem(ctx, p, "project.deviation.report", itemID); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Description) == "" {
		return "", ErrValidation
	}
	if err := validateProjectTextLength(input.Description, 1000, "偏离描述"); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.EvidenceURL) != "" {
		return "", ValidationError("偏离证据必须通过统一文件网关上传")
	}
	for i := range input.EvidenceFiles {
		if err := normalizeFileEvidence(&input.EvidenceFiles[i], "偏离证据"); err != nil {
			return "", err
		}
	}
	id := "DV-" + ulid.Make().String()
	return id, s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventDeviationReported, map[string]any{"deviation_id": id, "description": input.Description, "severity": input.Severity, "evidence_files": input.EvidenceFiles, "decision": "PENDING"}))
}

func normalizeFileEvidence(input *domain.ReportArtifactInput, label string) error {
	input.FileID, input.FileName, input.MIME, input.SHA256 = strings.TrimSpace(input.FileID), strings.TrimSpace(input.FileName), strings.TrimSpace(input.MIME), strings.ToLower(strings.TrimSpace(input.SHA256))
	if input.FileID == "" || input.FileName == "" || input.MIME == "" || input.Size == 0 || len(input.SHA256) != 64 {
		return ValidationError(label + "文件凭据不完整")
	}
	for _, ch := range input.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			return ValidationError(label + "文件 SHA-256 格式不正确")
		}
	}
	return nil
}

func (s *Service) ReviewDeviation(ctx context.Context, p platform.Principal, deviationID string, input domain.DeviationReviewInput) error {
	filter, err := authorizeProjectScope(p, "project.deviation.review")
	if err != nil {
		return err
	}
	repo, err := s.deliveryRepo()
	if err != nil {
		return err
	}
	projectID, itemID, err := repo.FindProjectForDeviation(ctx, filter, deviationID)
	if err != nil {
		return err
	}
	decision := strings.ToUpper(strings.TrimSpace(input.Decision))
	if decision != "RELEASE" && decision != "TERMINATE" && decision != "RETEST" {
		return ErrValidation
	}
	if err := validateProjectTextLength(input.Comment, 1000, "偏离处理意见"); err != nil {
		return err
	}
	if decision == "TERMINATE" && strings.TrimSpace(input.Comment) == "" {
		return ValidationError("终止服务项时必须填写终止原因")
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, itemID, EventDeviationReviewed, map[string]any{"deviation_id": deviationID, "decision": decision, "comment": input.Comment}))
}

// CompleteServiceItemField 确认单个服务项现场实施完成：服务项进入「现场实施完成」
// 并开启报告编制；全部服务项完成后项目状态由派生规则自动推进，不再有项目级一刀切
// 完成入口——多服务项项目里各服务项按自己的节奏收口。

// CompleteServiceItemField 确认单个服务项现场实施完成：服务项进入「现场实施完成」
// 并开启报告编制；全部服务项完成后项目状态由派生规则自动推进，不再有项目级一刀切
// 完成入口——多服务项项目里各服务项按自己的节奏收口。
func (s *Service) CompleteServiceItemField(ctx context.Context, p platform.Principal, itemID string, input domain.FieldCompletionInput) error {
	if err := s.authorizeServiceItem(ctx, p, "project.field.complete", itemID); err != nil {
		return err
	}
	if err := validateProjectTextLength(input.IncompleteReason, 1000, "现场完成缺项原因"); err != nil {
		return err
	}
	if err := s.verifyExpectedVersion(ctx, p, "project.field.complete", itemID, input.ExpectedVersion); err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldCompleted, map[string]any{
		"confirmed_by": p.UserID, "incomplete_reason": strings.TrimSpace(input.IncompleteReason), "expected_version": input.ExpectedVersion,
	}))
}
