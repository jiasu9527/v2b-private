-- 兼容旧部署的可选手工迁移；正常使用新版本 AI 运维接口时会自动执行相同迁移。
-- 空字符串表示未分组，不创建名为“未分组”的实际分组。
ALTER TABLE v2_ai_server ADD COLUMN IF NOT EXISTS group_name TEXT NOT NULL DEFAULT '';
UPDATE v2_ai_server SET group_name = '' WHERE group_name IS NULL;
ALTER TABLE v2_ai_server ALTER COLUMN group_name SET DEFAULT '';
ALTER TABLE v2_ai_server ALTER COLUMN group_name SET NOT NULL;
CREATE INDEX IF NOT EXISTS v2_ai_server_group_idx ON v2_ai_server(group_name);

ALTER TABLE v2_ai_server_intake ADD COLUMN IF NOT EXISTS group_name TEXT NOT NULL DEFAULT '';
UPDATE v2_ai_server_intake SET group_name = '' WHERE group_name IS NULL;
ALTER TABLE v2_ai_server_intake ALTER COLUMN group_name SET DEFAULT '';
ALTER TABLE v2_ai_server_intake ALTER COLUMN group_name SET NOT NULL;
