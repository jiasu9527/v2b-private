# DNSPod Domain Manager Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a secure admin DNSPod 3.0 domain and DNS-record manager to Forest.

**Architecture:** A small Go DNSPod client signs TC3 requests and is used by the existing admin service. New admin-only router handlers expose credential status, domains, records, dynamic types/lines, and record mutations; a React/Ant Design page consumes only these Forest APIs.

**Tech Stack:** Go 1.x standard library, existing Forest admin/session/config services, React 18, TypeScript, Ant Design 5, Vite.

---

### Task 1: DNSPod TC3 client

**Files:**
- Create: `go-api/internal/dnspod/client.go`
- Create: `go-api/internal/dnspod/client_test.go`

**Steps:**
1. Write failing tests for canonical TC3 authorization headers, response decoding, API error conversion, and HTTP timeout/cancellation.
2. Run `go test ./internal/dnspod -v` and confirm the package or symbols are missing.
3. Implement the minimal standard-library client for endpoint `dnspod.tencentcloudapi.com`, service `dnspod`, API version `2021-03-23`.
4. Add typed requests/results for domain list, record list, record types, record lines, create, modify, delete and status actions.
5. Run `go test ./internal/dnspod -v` and commit.

### Task 2: Admin DNSPod configuration and service

**Files:**
- Create: `go-api/internal/admin/dnspod.go`
- Create: `go-api/internal/admin/dnspod_test.go`
- Modify: `go-api/internal/admin/service.go`

**Steps:**
1. Write failing tests for environment precedence, saved credentials, masked SecretId, omitted SecretKey, credential clearing and request validation.
2. Add DNSPod request/result types and service methods to `admin.Service`.
3. Implement credential loading from environment and the existing admin config store.
4. Implement domain/record/type/line operations through an injected DNSPod client factory.
5. Run `go test ./internal/admin -run DNSPod -v` and commit.

### Task 3: Admin HTTP API

**Files:**
- Create: `go-api/internal/http/router_dnspod.go`
- Create: `go-api/internal/http/router_dnspod_test.go`
- Modify: `go-api/internal/http/router.go`
- Modify: `go-api/internal/http/router_test.go`

**Steps:**
1. Add fake admin-service DNSPod methods and failing route tests for authentication, methods, pagination, validation and mutation payloads.
2. Register the nine `/dns/...` routes under the existing dynamic admin prefix.
3. Implement handlers using the existing request decoding, authentication and JSON response conventions.
4. Verify `go test ./internal/http -run DNSPod -v` and commit.

### Task 4: React administration page

**Files:**
- Create: `admin-src/src/pages/DNSPodPage.tsx`
- Modify: `admin-src/src/App.tsx`
- Modify: `admin-src/src/styles/app.css`

**Steps:**
1. Add the “域名解析” navigation item and `/dns` page route.
2. Implement credential status and settings without ever rendering a saved SecretKey.
3. Implement responsive domain list with search, pagination and record navigation.
4. Implement record list, dynamic line/type loading, create/edit form, status toggle and delete confirmation.
5. Add focused styles consistent with the current black/white admin shell while preserving DNS status colors.
6. Run `npm run check` and commit.

### Task 5: Build and integration verification

**Files:**
- Modify generated bundle: `public/assets/admin-new/admin.js`

**Steps:**
1. Run `npm run build` to generate the production admin bundle.
2. Run `npm run check`.
3. Run `go test ./internal/dnspod ./internal/admin ./internal/http`.
4. Run `go test ./...` from `go-api`.
5. Inspect `git diff --check` and the final diff for leaked credentials.
6. Commit the generated bundle and push the feature commit(s) to the user repository.
