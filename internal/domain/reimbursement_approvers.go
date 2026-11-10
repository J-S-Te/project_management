package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

const (
	ExpenseNodeProjectOwner      = "PROJECT_EXPENSE_OWNER"
	ExpenseNodeTechnicalDirector = "TECHNICAL_DIRECTOR"
	ExpenseNodeSalesDirector     = "SALES_DIRECTOR"
)

// AttachExpenseApprovers 将当前权威节点目标及角色组织来源纳入费用引用版本。
// 参数：reference为资格与行程引用，targets为全部已核验审批目标，sourceDigest为完整解析边界的来源摘要。
// 返回值：稳定排序且版本覆盖角色、组织、集合和来源摘要变化的引用。
func AttachExpenseApprovers(reference ExpenseProjectReference, targets []ExpenseApprover, sourceDigest string) ExpenseProjectReference {
	targets = append([]ExpenseApprover{}, targets...)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].NodeRole != targets[j].NodeRole {
			return targets[i].NodeRole < targets[j].NodeRole
		}
		return targets[i].SubjectID < targets[j].SubjectID
	})
	raw, _ := json.Marshal(struct {
		ReferenceVersion string
		Targets          []ExpenseApprover
		Reasons          map[string][]string
		SourceDigest     string
	}{reference.ReferenceVersion, targets, reference.ApproverReasonCodes, sourceDigest})
	hash := sha256.Sum256(raw)
	reference.ReferenceVersion = hex.EncodeToString(hash[:])
	reference.RequiredApproverRefs = targets
	return reference
}
