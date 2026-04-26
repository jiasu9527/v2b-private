# Forest Client Entry Groups Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为 forest-app 新版补齐独立“客户端入口组”后台闭环，同时保持旧订阅、旧规则和 v2node 逻辑不变。

**Architecture:** 后端新增独立客户端入口组表与成员表，通过 admin API 管理入口组和成员关联；用户侧新增 forest runtime-profile 接口与入口 provider 接口，仅对加入入口组的节点额外下发入口信息，未加入入口组的节点继续按旧订阅逻辑工作。后台前端在已编译 `umi.js` 中新增“客户端入口”入口页，并在节点管理主表展示“所属入口组”。

**Tech Stack:** Go, PostgreSQL, sqlmock, net/http, compiled Umi admin bundle

---

### Task 1: 写后端红灯测试

**Files:**
- Modify: `/Users/anan/Documents/v2b/go-api/internal/http/router_client_test.go`
- Modify: `/Users/anan/Documents/v2b/go-api/internal/http/router_test.go`
- Create: `/Users/anan/Documents/v2b/go-api/internal/admin/client_entry_group_test.go`
- Create: `/Users/anan/Documents/v2b/go-api/internal/user/client_entry_group_test.go`

**Step 1: 写 runtime-profile / provider / admin CRUD 失败测试**

**Step 2: 运行相关 go test，确认因为缺接口或缺实现失败**

**Step 3: 保持测试只描述新行为，不改旧接口期望**

### Task 2: 实现数据库与 service

**Files:**
- Modify: `/Users/anan/Documents/v2b/database/install.pgsql.sql`
- Modify: `/Users/anan/Documents/v2b/database/update.pgsql.sql`
- Modify: `/Users/anan/Documents/v2b/go-api/internal/admin/service.go`
- Modify: `/Users/anan/Documents/v2b/go-api/internal/user/service.go`
- Create: `/Users/anan/Documents/v2b/go-api/internal/admin/client_entry_group.go`
- Create: `/Users/anan/Documents/v2b/go-api/internal/user/client_entry_group.go`

**Step 1: 新增客户端入口组主表与成员表**

**Step 2: 新增 admin service 类型与 CRUD**

**Step 3: 新增 user service 读取入口组与成员引用**

**Step 4: 给节点列表聚合所属入口组名称**

### Task 3: 实现 HTTP 路由与 handler

**Files:**
- Modify: `/Users/anan/Documents/v2b/go-api/internal/http/router.go`
- Create: `/Users/anan/Documents/v2b/go-api/internal/http/router_client_forest.go`
- Modify: `/Users/anan/Documents/v2b/go-api/internal/http/router_client.go`

**Step 1: 新增 admin `/client-entry/*` 路由**

**Step 2: 新增用户 `/api/v1/user/forest/runtime-profile`**

**Step 3: 新增公网 `/api/v1/client/forest/entry-provider`**

**Step 4: provider YAML 复用现有 Clash proxy 构建逻辑**

### Task 4: 修改后台已编译 bundle

**Files:**
- Modify: `/Users/anan/Documents/v2b/public/assets/admin/umi.js`

**Step 1: 增加“客户端入口”菜单和路由**

**Step 2: 先复用现有 route 管理页面骨架接新接口**

**Step 3: 给节点管理主表增加“所属入口组”列**

### Task 5: 验证

**Files:**
- Modify: `/Users/anan/Documents/v2b/go-api/internal/http/router_client_test.go`
- Modify: `/Users/anan/Documents/v2b/go-api/internal/http/router_test.go`

**Step 1: 运行新增测试，确认先红后绿**

**Step 2: 跑相关包测试**

**Step 3: 若 bundle 改动引入错误，记录并修复**
