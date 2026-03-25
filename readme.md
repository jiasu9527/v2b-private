# v2b-private

基于 V2Board 的私有维护版仓库，用于保存当前可部署程序快照。

这个仓库的定位不是上游镜像，而是实际运行版本的代码归档和持续维护仓库。除了 Laravel 后端代码外，也包含当前直接部署用到的已编译前端资源。

## 仓库内容

- Laravel 后端代码
- 已编译后台资源：`public/assets/admin/umi.js`
- 已编译前台主题资源：`public/theme/default/assets/`
- 数据库安装与升级脚本：`database/install.sql`、`database/update.sql`
- 本仓库内新增的功能测试：`tests/Feature/`

## 当前包含的定制功能

- 邀请活动任务系统
- 管理后台邀请活动页面
- 仪表盘新增统计项
- 订单取消后的自动补单与后台补单
- 节点管理批量修改节点地址

## 运行依赖

- PHP `^7.3|^8.0`
- Composer
- MySQL / MariaDB
- Redis
- Nginx 或 Apache

建议实际部署使用 PHP 8.1 及以上。

## 新环境部署

1. 安装依赖

```bash
composer install --no-dev -o
```

2. 复制环境文件

```bash
cp .env.example .env
```

3. 修改 `.env`

至少配置这些项目：

- `APP_URL`
- `APP_KEY`
- `DB_*`
- `REDIS_*`
- 邮件和支付相关配置

4. 生成应用密钥

```bash
php artisan key:generate
```

5. 初始化数据库

全新安装可优先使用：

```bash
mysql -u <user> -p <database> < database/install.sql
php artisan migrate --force
```

如果是已有站点升级，在备份数据库后执行：

```bash
mysql -u <user> -p <database> < database/update.sql
php artisan migrate --force
```

6. 处理目录权限

确保这些目录可写：

- `storage/`
- `bootstrap/cache/`

7. 生成缓存并启动队列

```bash
php artisan config:clear
php artisan config:cache
php artisan horizon
```

## 常用命令

```bash
composer install
php artisan migrate --force
php artisan horizon
php artisan test
```

单独跑关键功能测试：

```bash
vendor/bin/phpunit tests/Feature/AdminServerBulkHostUpdateTest.php --testdox
vendor/bin/phpunit tests/Feature/OrderCancelRecoveryTest.php --testdox
vendor/bin/phpunit tests/Feature/InviteCampaignPageRouteTest.php --testdox
```

## 目录说明

- `app/`：核心业务逻辑
- `app/Http/Controllers/V1/Admin/`：后台接口
- `app/Services/`：业务服务层
- `database/`：安装 SQL、升级 SQL、迁移文件
- `public/assets/admin/`：当前后台已编译资源
- `public/theme/default/assets/`：当前用户侧已编译资源
- `resources/views/`：Blade 模板
- `tests/Feature/`：功能回归测试

## 前端资源说明

这个仓库当前追踪的是“可直接部署”的编译产物，而不是纯源码仓库。

尤其是：

- `public/assets/admin/umi.js`
- `public/theme/default/assets/umi.js`

如果后台或前台页面是直接改编译文件，这些改动必须一起提交，否则服务器上的实际功能会丢失。

## 提交与安全规则

不要提交以下内容：

- `.env`
- `.env.*`（`*.example` 除外）
- `vendor/`
- `node_modules/`
- 运行日志、缓存、会话文件
- 本地数据库导出、备份文件、临时脚本

仓库已通过 `.gitignore` 排除大多数环境文件，但提交前仍建议手动检查一次：

```bash
git status --short
git diff --stat
```

## 敏感信息排查建议

提交前建议执行：

```bash
git ls-files -z | xargs -0 rg -n -H "(ghp_|github_pat_|sk_live_|AKIA|BEGIN (RSA|OPENSSH|EC|DSA) PRIVATE KEY|Authorization: Bearer )"
```

如果命中的是示例代码、字符串模板或测试桩，需要人工确认；如果命中真实凭据，先删除再提交。

## 说明

- 本仓库是私有维护仓库，不作为公开发行版说明文档使用
- 上游许可证见 `LICENSE`
