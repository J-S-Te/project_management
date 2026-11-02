package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/j-s-te/project-management/internal/businessclock"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"io"
	"math"
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
		// 行程单号里的日期段按北京业务日：跨零点后新开的行程必须落在新日期，
		// 否则同一段业务时间里会混出两个日期段的单号。
		travel.RequestID = newTravelRequestID(time.Now())
		travel.ReferenceEventID = ""
		travel.NoTravelReason = ""
		return travel, nil
	default:
		return domain.TravelArrangementInput{}, ValidationError("请选择有效的行程安排方式")
	}
}

// newTravelRequestID 生成 TRIP-<北京业务日>-<后缀> 行程单号。
func newTravelRequestID(now time.Time) string {
	return "TRIP-" + businessclock.DateString(now) + "-" + strings.ToUpper(ulid.Make().String()[20:])
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
		if err := s.verifyFileEvidence(ctx, p, itemID, input.EvidenceFiles[i], "现场证据"); err != nil {
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
	filter, err := authorizeProjectScope(p, "project.field.execute")
	if err != nil {
		return err
	}
	item, err := s.Repo.GetServiceItem(ctx, filter, itemID)
	if err != nil {
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
	// BIZ-PROJECT-05：签到坐标必须与站点主数据一致；偏离或无法核验时偏离原因不能为空。
	policy := s.fieldCheckInPolicy()
	assessment, err := s.assessCheckInSite(ctx, p.TenantID, item, input.Latitude, input.Longitude)
	if err != nil {
		return err
	}
	if err := requireCheckInDeviationReason(assessment, input.DeviationReason, policy); err != nil {
		return err
	}
	operationID, capturedAt, err := normalizeOfflineOperation(input.ClientOperationID, input.CapturedAt, true)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"latitude": input.Latitude, "longitude": input.Longitude, "accuracy_meters": input.AccuracyMeters,
		"deviation_reason": strings.TrimSpace(input.DeviationReason), "expected_version": input.ExpectedVersion,
		"site_check": assessment.Status,
	}
	// 比对结论一并留痕：只有"未关联站点主数据"的历史数据没有站点维度，其余都写清楚
	// 站点、实际距离与阈值，审计时能直接判断这条签到是否可信。
	if assessment.SiteCode != "" {
		payload["site_code"] = assessment.SiteCode
	}
	if assessment.SiteName != "" {
		payload["site_name"] = assessment.SiteName
	}
	if assessment.hasDistance() {
		payload["site_distance_meters"] = math.Round(assessment.DistanceMeters*10) / 10
		payload["site_distance_limit_meters"] = assessment.LimitMeters
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
	if err := s.verifyFileEvidence(ctx, p, itemID, input.SignatureFile, "电子签名"); err != nil {
		return err
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
		if err := s.verifyFileEvidence(ctx, p, itemID, input.EvidenceFiles[i], "偏离证据"); err != nil {
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

// evidenceResourceType 是本子系统在文件网关登记业务对象时使用的资源类型：
// 现场记录/电子签名/偏离/报告证据都绑定到具体服务项（resource_id = itemID）。
const evidenceResourceType = "PROJECT_SERVICE_ITEM"

// verifyFileEvidence 回源文件网关核验一份证据回执（BIZ-PROJECT-03）。
//
// 修复前这里只校验 file_id/sha256/大小这类"回执形状"，伪造回执即可满足现场完成与
// 报告签发门禁，证据台账也会指向不存在的文件。现在必须确认网关侧的只读元数据：
// 文件真实存在且仍可用、归属同一租户、绑定到本次提交的服务项（对象级绑定，只看
// 应用级绑定会让服务项 A 的证据被复用到服务项 B）、服务端摘要/大小/类型与回执一致。
// 任何一项不满足都拒绝，不写事件、不登记证据。
// 网关未配置或不可达时同样失败关闭：没有核验结论就不能当成"已核验"。
func (s *Service) verifyFileEvidence(ctx context.Context, p platform.Principal, itemID string, artifact domain.ReportArtifactInput, label string) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return ValidationError(label + "缺少服务项标识，无法核验证据归属")
	}
	if s.EvidenceFiles == nil {
		return evidenceGatewayUnavailable(label)
	}
	metadata, err := s.EvidenceFiles.StatEvidence(ctx, artifact.FileID, evidenceResourceType, itemID)
	if err != nil {
		if errors.Is(err, platform.ErrEvidenceFileUnavailable) {
			return ValidationError(label + "文件未在文件网关登记、已不可用、不属于当前应用或未绑定到当前服务项")
		}
		return evidenceGatewayUnavailable(label)
	}
	if strings.TrimSpace(metadata.FileID) != artifact.FileID {
		return ValidationError(label + "文件与文件网关返回的文件不一致")
	}
	if tenant := strings.TrimSpace(p.TenantID); tenant != "" && !strings.EqualFold(strings.TrimSpace(metadata.TenantID), tenant) {
		return ValidationError(label + "文件不属于当前租户")
	}
	if !evidenceStatusAvailable(metadata.Status) {
		return ValidationError(label + "文件当前不可用")
	}
	if !metadata.BindingActive {
		return ValidationError(label + "文件未绑定到当前业务对象")
	}
	if metadata.SizeBytes != artifact.Size {
		return ValidationError(label + "文件大小与文件网关记录不一致")
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.SHA256), artifact.SHA256) {
		return ValidationError(label + "文件摘要与文件网关记录不一致")
	}
	// 类型同样要回源核对：否则把 PDF 声明成 image/png 就能冒充电子签名。
	if declared := canonicalEvidenceMediaType(artifact.MIME); declared != "" && strings.TrimSpace(metadata.MediaType) != "" && declared != strings.TrimSpace(metadata.MediaType) {
		return ValidationError(label + "文件类型与文件网关记录不一致")
	}
	return nil
}

// canonicalEvidenceMediaType 归一化证据 MIME（去参数、转小写），供回执与网关记录比对。
func canonicalEvidenceMediaType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(strings.TrimSpace(value), ";")[0]))
}

// evidenceStatusAvailable 判断网关返回的文件状态是否仍可用于业务证据。
// 仓储只会解析可用状态，这里仍显式列出，避免网关未来放宽查询时静默放行。
func evidenceStatusAvailable(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "READY", "AVAILABLE", "ACTIVE":
		return true
	default:
		return false
	}
}

// evidenceGatewayUnavailable 表示证据无法核验（网关未配置或不可达）。
// 复用 ErrServiceTimeout 的依赖不可用语义（接口层映射为 503 可重试），
// 绝不放行未经核验的证据，也不把未知状态当成"通过"。
func evidenceGatewayUnavailable(label string) error {
	return ReasonError{Kind: ErrServiceTimeout, Reason: "文件网关暂不可用，无法核验" + label + "，请稍后重试"}
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
//
// BIZ-PROJECT-06：缺项判定与写入同源。完成事件里的缺项说明必须与本服务项的权威证据链
// 一致：证据不全却说明为空是"无据完成"，证据齐全却写缺项说明是"与事实相反的审计叙述"。
// 两种矛盾都由服务端门禁拦下，判定结果（gaps 快照与 evidence_complete）随事件落库。
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
	filter, err := authorizeProjectScope(p, "project.field.complete")
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(input.IncompleteReason)
	payload := map[string]any{"confirmed_by": p.UserID, "incomplete_reason": reason, "expected_version": input.ExpectedVersion}
	policy := s.fieldEvidenceGatePolicy()
	evidence, evidenceErr := s.serviceItemFieldEvidence(ctx, filter, itemID)
	if evidenceErr != nil {
		// 判定不可用（事件超限、仓储不支持、读取失败）时不能假装"证据齐全"：
		// enforce 直接拒绝；advisory 只记录并保留"未知"痕迹，绝不写 evidence_complete=true。
		if policy == FieldIncompleteReasonGateEnforce {
			return evidenceErr
		}
		s.logFieldEvidenceGate("evidence unavailable; completion allowed in advisory mode", itemID, reason, "error", evidenceErr.Error())
		payload["evidence_state"] = FieldEvidenceStateUnknown
		payload["evidence_error"] = evidenceErr.Error()
	} else {
		payload["gaps"] = evidence.Gaps
		payload["missing_evidence"] = evidence.Missing
		payload["evidence_complete"] = evidence.EvidenceComplete
		payload["evidence_state"] = evidence.State
		if err := s.requireFieldEvidenceReason(policy, evidence, reason); err != nil {
			return err
		}
	}
	return s.applyEvent(ctx, deliveryEvent(p, "", itemID, EventFieldCompleted, payload))
}

// FieldEvidenceStateUnknown 表示服务端未能完成缺项判定（证据不可用/未同步）。
// 前端必须据此 fail-closed：未知不等于齐全。
const FieldEvidenceStateUnknown = "UNKNOWN"

// MaxServiceItemDeliveryEvents 是单服务项事件流的读取上限（BIZ-PROJECT-06）。
// 超限必须报错而不是静默截断：截断会让缺项判定重新退化成窗口近似，正是本缺陷的根因。
const MaxServiceItemDeliveryEvents = 1000

var (
	// ErrFieldEvidenceWindowExceeded 表示该服务项的事件数超过单次可核验上限，
	// 判定结果不可信，读写都必须拒绝而不是按部分事件下结论。
	ErrFieldEvidenceWindowExceeded = errors.New("service item evidence event window exceeded")
	// ErrFieldEvidenceUnavailable 表示仓储不具备按服务项读取完整事件流的能力，
	// 无法核验缺项。宁可拒绝，也不能按"证据齐全"放行。
	ErrFieldEvidenceUnavailable = errors.New("service item evidence is unavailable")
)

// FieldEvidenceStateComplete / FieldEvidenceStateIncomplete 是判定完成后的两种结论。
const (
	FieldEvidenceStateComplete   = "COMPLETE"
	FieldEvidenceStateIncomplete = "INCOMPLETE"
)

// 缺项说明门禁的两种强度（BIZ-PROJECT-06）。
const (
	// FieldIncompleteReasonGateAdvisory 只记录不拒绝：存量升级先跑该档，
	// 等前端改为服务端判定后再切 enforce。
	FieldIncompleteReasonGateAdvisory = "ADVISORY"
	// FieldIncompleteReasonGateEnforce 执行 422 拒绝。新装默认该档。
	FieldIncompleteReasonGateEnforce = "ENFORCE"
)

// FieldEvidenceGatePolicy 是现场完成缺项说明门禁的策略。
// 零值等价于 enforce：无法识别的配置不能当成"不校验"。
type FieldEvidenceGatePolicy struct {
	Mode string
}

// normalize 补齐默认值并收敛未知取值。
func (policy FieldEvidenceGatePolicy) normalize() string {
	if strings.ToUpper(strings.TrimSpace(policy.Mode)) == FieldIncompleteReasonGateAdvisory {
		return FieldIncompleteReasonGateAdvisory
	}
	return FieldIncompleteReasonGateEnforce
}

func (s *Service) fieldEvidenceGatePolicy() string { return s.FieldEvidenceGate.normalize() }

// ServiceItemEventRepository 是服务项事件流的窄接口：缺项判定必须基于该服务项的完整
// 证据链，而不是"租户最近 N 条事件"的窗口。与 SiteRepository 一样按需断言，让只实现
// 部分能力的仓储（含测试桩）继续可用；能力缺失时按"无法核验"处理，绝不静默放行。
type ServiceItemEventRepository interface {
	ListServiceItemDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error)
}

// fieldEvidenceRequirement 是现场完成必须具备的一类证据。
type fieldEvidenceRequirement struct {
	Type  string
	Label string
}

// fieldEvidenceRequirements 固定三类必备证据，顺序即界面展示顺序。
var fieldEvidenceRequirements = []fieldEvidenceRequirement{
	{Type: EventFieldCheckedIn, Label: "GPS 签到"},
	{Type: EventFieldRecordSubmitted, Label: "现场记录"},
	{Type: EventFieldSignatureCaptured, Label: "电子签名"},
}

// FieldEvidenceEntry 是一条已经具备的现场证据，直接回给界面展示，
// 让"真实证据在 UI 上不可见"不再成立。
type FieldEvidenceEntry struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	EventID     string `json:"event_id"`
	ActorUserID string `json:"actor_user_id,omitempty"`
	CapturedAt  string `json:"captured_at,omitempty"`
	FileRef     string `json:"file_ref,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// ServiceItemFieldEvidence 是单个服务项的现场证据完整性判定结果。
type ServiceItemFieldEvidence struct {
	ServiceItemID string `json:"service_item_id"`
	// Gaps 是缺失证据的类型码，Missing 是同序的中文名称，界面直接展示 Missing。
	Gaps     []string             `json:"gaps"`
	Missing  []string             `json:"missing"`
	Evidence []FieldEvidenceEntry `json:"evidence"`
	// EvidenceComplete 为 true 时表示三类证据齐全，不得再填写缺项说明。
	EvidenceComplete bool   `json:"evidence_complete"`
	State            string `json:"state"`
	GatePolicy       string `json:"gate_policy"`
	// ScannedEvents 是参与判定的该服务项事件总数，便于核对是否触达读取上限。
	ScannedEvents int `json:"scanned_events"`
}

// ServiceItemFieldEvidence 返回服务项的现场证据完整性与已具备的证据明细。
// 读取权限 + 资源授权与其它只读接口一致（project.read + 数据范围）。
func (s *Service) ServiceItemFieldEvidence(ctx context.Context, p platform.Principal, itemID string) (ServiceItemFieldEvidence, error) {
	if err := s.authorizeServiceItem(ctx, p, "project.read", itemID); err != nil {
		return ServiceItemFieldEvidence{}, err
	}
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return ServiceItemFieldEvidence{}, err
	}
	return s.serviceItemFieldEvidence(ctx, filter, itemID)
}

// serviceItemFieldEvidence 以权威事件流计算缺项，供读接口与完成门禁共用同一口径。
func (s *Service) serviceItemFieldEvidence(ctx context.Context, filter platform.ScopeFilter, itemID string) (ServiceItemFieldEvidence, error) {
	repo, ok := s.Repo.(ServiceItemEventRepository)
	if !ok {
		return ServiceItemFieldEvidence{}, ErrFieldEvidenceUnavailable
	}
	events, err := repo.ListServiceItemDeliveryEvents(ctx, filter, itemID)
	if err != nil {
		return ServiceItemFieldEvidence{}, err
	}
	evidence := make([]FieldEvidenceEntry, 0, len(fieldEvidenceRequirements))
	present := make(map[string]bool, len(fieldEvidenceRequirements))
	for _, event := range events {
		requirement, known := fieldEvidenceRequirementOf(event.Type)
		if !known {
			continue
		}
		// 判定只看"是否出现过"；明细保留最近一条，更贴近现场事实。
		entry := FieldEvidenceEntry{
			Type: requirement.Type, Label: requirement.Label, EventID: event.ID,
			ActorUserID: event.ActorUserID, CapturedAt: fieldEvidenceCapturedAt(event),
			FileRef: fieldEvidenceFileRef(event), Summary: fieldEvidenceSummary(event),
		}
		if index := fieldEvidenceIndex(evidence, requirement.Type); index >= 0 {
			evidence[index] = entry
		} else {
			evidence = append(evidence, entry)
		}
		present[requirement.Type] = true
	}
	gaps := make([]string, 0, len(fieldEvidenceRequirements))
	missing := make([]string, 0, len(fieldEvidenceRequirements))
	for _, requirement := range fieldEvidenceRequirements {
		if present[requirement.Type] {
			continue
		}
		gaps = append(gaps, requirement.Type)
		missing = append(missing, requirement.Label)
	}
	state := FieldEvidenceStateComplete
	if len(gaps) > 0 {
		state = FieldEvidenceStateIncomplete
	}
	return ServiceItemFieldEvidence{
		ServiceItemID: itemID, Gaps: gaps, Missing: missing, Evidence: evidence,
		EvidenceComplete: len(gaps) == 0, State: state, GatePolicy: s.fieldEvidenceGatePolicy(),
		ScannedEvents: len(events),
	}, nil
}

// requireFieldEvidenceReason 执行缺项说明与证据判定的一致性门禁（四象限）：
//   - 证据齐全 + 无说明 → 放行；
//   - 证据齐全 + 有说明 → 拒绝（阻止"证据齐全却写缺项"的矛盾叙述）；
//   - 证据不全 + 有说明 → 放行（确认无法补齐的合法说明）；
//   - 证据不全 + 无说明 → 拒绝（无据完成）。
//
// advisory 档只记录不拒绝，用于存量升级的过渡窗口。
func (s *Service) requireFieldEvidenceReason(policy string, evidence ServiceItemFieldEvidence, reason string) error {
	if evidence.EvidenceComplete && reason != "" {
		if policy == FieldIncompleteReasonGateEnforce {
			return ValidationError("现场证据已齐全（" + fieldEvidenceLabelList() + "），不得填写缺项说明；请清空说明后重新确认完成")
		}
		s.logFieldEvidenceGate("evidence complete but a gap reason was submitted", evidence.ServiceItemID, reason)
		return nil
	}
	if !evidence.EvidenceComplete && reason == "" {
		if policy == FieldIncompleteReasonGateEnforce {
			return ValidationError("现场证据不完整，缺少 " + strings.Join(evidence.Missing, "、") + "；请先补齐证据，确实无法补齐时填写缺项确认说明")
		}
		s.logFieldEvidenceGate("evidence incomplete without a gap reason", evidence.ServiceItemID, reason, "missing", strings.Join(evidence.Missing, "、"))
	}
	return nil
}

func (s *Service) logFieldEvidenceGate(message, itemID, reason string, attributes ...any) {
	if s.Logger == nil {
		return
	}
	fields := []any{"service_item_id", itemID, "incomplete_reason", reason, "gate", s.fieldEvidenceGatePolicy()}
	fields = append(fields, attributes...)
	s.Logger.Warn("field-incomplete-reason gate: "+message, fields...)
}

// fieldEvidenceLabelList 返回三类必备证据的中文清单，用于提示文案。
func fieldEvidenceLabelList() string {
	labels := make([]string, 0, len(fieldEvidenceRequirements))
	for _, requirement := range fieldEvidenceRequirements {
		labels = append(labels, requirement.Label)
	}
	return strings.Join(labels, "、")
}

func fieldEvidenceRequirementOf(eventType string) (fieldEvidenceRequirement, bool) {
	for _, requirement := range fieldEvidenceRequirements {
		if requirement.Type == eventType {
			return requirement, true
		}
	}
	return fieldEvidenceRequirement{}, false
}

func fieldEvidenceIndex(evidence []FieldEvidenceEntry, eventType string) int {
	for index, entry := range evidence {
		if entry.Type == eventType {
			return index
		}
	}
	return -1
}

// fieldEvidenceCapturedAt 优先取事件列的采集时间，历史事件回退到载荷里的 captured_at。
func fieldEvidenceCapturedAt(event domain.DeliveryEvent) string {
	if event.CapturedAt != nil {
		return event.CapturedAt.UTC().Format(time.RFC3339)
	}
	if captured := payloadText(event.Payload, "captured_at"); captured != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, captured); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
		return captured
	}
	return ""
}

// fieldEvidenceFileRef 提取证据引用的文件网关回执 ID。
// 落库后 JSON 反序列化得到 map，测试桩里可能是结构体，两种形态都要能读。
func fieldEvidenceFileRef(event domain.DeliveryEvent) string {
	switch event.Type {
	case EventFieldSignatureCaptured:
		return firstEvidenceFileRef(event.Payload["signature_file"], event.Payload["evidence_files"])
	case EventFieldRecordSubmitted:
		return firstEvidenceFileRef(event.Payload["evidence_files"])
	}
	return ""
}

func firstEvidenceFileRef(values ...any) string {
	for _, value := range values {
		if ref := evidenceFileRefOf(value); ref != "" {
			return ref
		}
	}
	return ""
}

func evidenceFileRefOf(value any) string {
	switch typed := value.(type) {
	case domain.ReportArtifactInput:
		return strings.TrimSpace(typed.FileID)
	case []domain.ReportArtifactInput:
		if len(typed) > 0 {
			return strings.TrimSpace(typed[0].FileID)
		}
	case map[string]any:
		return payloadText(typed, "file_id")
	case []any:
		if len(typed) > 0 {
			return evidenceFileRefOf(typed[0])
		}
	case []map[string]any:
		if len(typed) > 0 {
			return payloadText(typed[0], "file_id")
		}
	}
	return ""
}

// fieldEvidenceSummary 只对签到给出可读摘要（坐标与精度），其余类型由 file_ref 表达。
func fieldEvidenceSummary(event domain.DeliveryEvent) string {
	if event.Type != EventFieldCheckedIn {
		return ""
	}
	latitude, latitudeOK := event.Payload["latitude"].(float64)
	longitude, longitudeOK := event.Payload["longitude"].(float64)
	if !latitudeOK || !longitudeOK {
		return ""
	}
	summary := fmt.Sprintf("定位 %.4f, %.4f", latitude, longitude)
	if accuracy, ok := event.Payload["accuracy_meters"].(float64); ok && accuracy > 0 {
		summary += fmt.Sprintf("（精度 %.1f 米）", accuracy)
	}
	return summary
}
