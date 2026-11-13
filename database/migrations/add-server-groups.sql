-- 添加服务器分组功能
-- 执行时间：2026-11-14

-- 1. 为 v2_ai_server 表添加分组字段
ALTER TABLE v2_ai_server ADD COLUMN IF NOT EXISTS group_name VARCHAR(100);

-- 2. 为 v2_ai_server_intake 表添加分组字段
ALTER TABLE v2_ai_server_intake ADD COLUMN IF NOT EXISTS group_name VARCHAR(100);

-- 3. 创建分组索引以提升查询性能
CREATE INDEX IF NOT EXISTS idx_ai_server_group ON v2_ai_server(group_name) WHERE group_name IS NOT NULL;

-- 4. 添加注释
COMMENT ON COLUMN v2_ai_server.group_name IS '服务器分组名称，用于组织管理多个服务器';
COMMENT ON COLUMN v2_ai_server_intake.group_name IS '服务器注册时的分组名称';

-- 示例：更新现有服务器到默认分组（可选）
-- UPDATE v2_ai_server SET group_name = '未分组' WHERE group_name IS NULL;
