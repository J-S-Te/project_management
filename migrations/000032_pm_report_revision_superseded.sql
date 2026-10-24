-- Report return-to-field preserves the old revision as history. The original
-- check constraint only allowed ACTIVE/VOID, so the documented SUPERSEDED path
-- failed at runtime. Allow the historical state without deleting evidence.
ALTER TABLE pm_report_revision
  DROP CHECK chk_pm_report_revision_validity;
ALTER TABLE pm_report_revision
  ADD CONSTRAINT chk_pm_report_revision_validity
  CHECK (validity_status IN ('ACTIVE','VOID','SUPERSEDED'));
