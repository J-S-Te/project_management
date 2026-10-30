package domain

// ContractImportBindingCommand 是冻结策略下的项目接续命令。
type ContractImportBindingCommand struct {
	SchemaVersion          int                `json:"schema_version"`
	CommandID              string             `json:"command_id"`
	TenantID               string             `json:"tenant_id"`
	ContractID             string             `json:"contract_id"`
	ContractVersion        string             `json:"contract_version"`
	OriginImportItemID     string             `json:"origin_import_item_id"`
	SourceRevision         uint64             `json:"source_revision"`
	PolicyDigest           string             `json:"policy_digest"`
	ReleaseVersion         uint64             `json:"release_version"`
	ImportMode             string             `json:"import_mode"`
	ProjectMode            string             `json:"project_mode"`
	ExistingProjectID      string             `json:"existing_project_id,omitempty"`
	ExpectedProjectVersion uint64             `json:"expected_project_version,omitempty"`
	Relationship           string             `json:"relationship"`
	OriginalContractRef    string             `json:"original_contract_ref,omitempty"`
	TargetOwnerUserID      string             `json:"target_owner_user_id"`
	Contract               ContractActivation `json:"contract"`
}

// ContractImportBindingReceipt 是与项目及来源映射一起提交的完成证据。
type ContractImportBindingReceipt struct {
	CommandID          string `json:"command_id"`
	SchemaVersion      int    `json:"schema_version"`
	TenantID           string `json:"tenant_id"`
	OriginImportItemID string `json:"origin_import_item_id"`
	SourceRevision     uint64 `json:"source_revision"`
	PolicyDigest       string `json:"policy_digest"`
	ReleaseVersion     uint64 `json:"release_version"`
	ContractID         string `json:"contract_id"`
	ProjectID          string `json:"project_id"`
	ProjectVersion     uint64 `json:"project_version"`
	Status             string `json:"status"`
}
