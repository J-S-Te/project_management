-- 检测类别域的「必检能力码（默认）」：原型「必备资质（默认）」是资质名称（如 CISP-PTE），
-- 而项目系统的能力校验比对的是能力码（pm_capability.capability_codes）。两者不是同一套编码，
-- 因此单独给出能力码字段：填写后，按该类别拆解生成的服务项会带上这些必检能力码，
-- 分配工程师时按此校验；留空则维持原行为（只按渗透测试追加 PENETRATION_TEST）。
-- 默认留空，避免在既有能力码目录之外凭空造码导致分配一律判冲突。
ALTER TABLE pm_detection_category
  ADD COLUMN required_codes VARCHAR(255) NOT NULL DEFAULT '' AFTER required_qualifications;
