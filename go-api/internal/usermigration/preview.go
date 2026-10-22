package usermigration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/mail"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/config"
	"golang.org/x/crypto/bcrypt"
)

type DBService struct {
	cfg        config.Config
	runtime    *config.RuntimeState
	db         *sql.DB
	mu         sync.Mutex
	previews   map[string]*storedPreview
	gate       chan struct{}
	ensureMu   sync.Mutex
	ensured    bool
	now        func() time.Time
	openSource func(SourceConfig) (*sql.DB, error)
}

func NewDBService(cfg config.Config, db *sql.DB) *DBService {
	return &DBService{cfg: cfg, db: db, previews: map[string]*storedPreview{}, gate: make(chan struct{}, 1), now: time.Now, openSource: openSource}
}
func (s *DBService) WithRuntimeConfig(r *config.RuntimeState) *DBService { s.runtime = r; return s }
func (s *DBService) resetMethod() int64 {
	if s.runtime != nil {
		return s.runtime.CurrentConfig().ResetTrafficMethod
	}
	return s.cfg.ResetTrafficMethod
}
func (s *DBService) enter() error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	select {
	case s.gate <- struct{}{}:
		return nil
	default:
		return ErrBusy
	}
}
func (s *DBService) leave() { <-s.gate }

func (s *DBService) Inspect(ctx context.Context, actorID int64, c SourceConfig) (Inspection, error) {
	if actorID <= 0 {
		return Inspection{}, ErrInvalidParameter
	}
	if err := s.enter(); err != nil {
		return Inspection{}, err
	}
	defer s.leave()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	snap, err := s.sourceSnapshot(ctx, c)
	if err != nil {
		return Inspection{}, err
	}
	plans, err := readTargetPlans(ctx, s.db)
	if err != nil {
		return Inspection{}, err
	}
	out := Inspection{TotalUsers: len(snap.users), SourcePlans: snap.plans, TargetPlans: sortedPlans(plans), Warnings: []string{"仅迁移普通用户；源管理员、员工排除，目标站已有用户保持不变。", "源数据库凭据仅用于本次读取，不写入数据库、文件或日志。"}}
	if out.SourcePlans == nil {
		out.SourcePlans = []Plan{}
	}
	for _, u := range snap.users {
		if u.IsAdmin != 0 {
			out.SourceAdmins++
		}
		if u.IsStaff != 0 {
			out.SourceStaff++
		}
		if u.LockingBalance > 0 {
			out.LockingBalanceUsers++
			if math.MaxInt64-out.LockingBalanceTotal < u.LockingBalance {
				return Inspection{}, ErrSourceSchema
			}
			out.LockingBalanceTotal += u.LockingBalance
		}
	}
	return out, nil
}

func (s *DBService) Preview(ctx context.Context, actorID int64, req PreviewRequest) (PreviewResult, error) {
	if actorID <= 0 {
		return PreviewResult{}, ErrInvalidParameter
	}
	policy := strings.TrimSpace(req.LockingBalancePolicy)
	if policy == "" {
		policy = "skip"
	}
	if policy != "skip" && policy != "discard" && policy != "to_balance" {
		return PreviewResult{}, ErrInvalidParameter
	}
	if policy != "skip" && !req.ConfirmLockingBalance {
		return PreviewResult{}, ErrInvalidParameter
	}
	mappings := map[int64]int64{}
	for src, dst := range req.PlanMapping {
		id, err := strconv.ParseInt(src, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != src || dst < 0 || dst > math.MaxInt32 {
			return PreviewResult{}, ErrInvalidParameter
		}
		mappings[id] = dst
	}
	if err := s.enter(); err != nil {
		return PreviewResult{}, err
	}
	defer s.leave()
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	s.mu.Lock()
	s.pruneLocked()
	for id, p := range s.previews {
		if p.actorID != actorID {
			continue
		}
		if p.state == "unknown" {
			s.mu.Unlock()
			return PreviewResult{}, ErrCommitUnknown
		}
		if p.state == "applying" {
			s.mu.Unlock()
			return PreviewResult{}, ErrBusy
		}
		// A new preview replaces this administrator's older drafts. This also
		// allows correcting blocked mappings without waiting for the TTL.
		if p.expiryTimer != nil {
			p.expiryTimer.Stop()
		}
		p.users = nil
		delete(s.previews, id)
	}
	full := len(s.previews) >= maxPreviews
	s.mu.Unlock()
	if full {
		return PreviewResult{}, ErrBusy
	}
	snap, err := s.sourceSnapshot(ctx, req.Source)
	req.Source.Password = ""
	if err != nil {
		return PreviewResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return PreviewResult{}, ErrTargetRead
	}
	defer tx.Rollback()
	target, err := readTargetState(ctx, tx)
	if err != nil {
		return PreviewResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return PreviewResult{}, ErrTargetRead
	}
	for _, dst := range mappings {
		if dst > 0 {
			if _, ok := target.plans[dst]; !ok {
				return PreviewResult{}, ErrInvalidParameter
			}
		}
	}
	now := s.now()
	regenerate := regenerationOptions{tokens: req.RegenerateTokens, uuids: req.RegenerateUUIDs}
	result, users, usedPlans, err := buildPreview(snap, target, mappings, policy, regenerate)
	if err != nil {
		return PreviewResult{}, err
	}
	id, err := randomHex(32)
	if err != nil {
		return PreviewResult{}, ErrUnavailable
	}
	result.PreviewID = id
	result.ExpiresAt = now.Add(PreviewTTL).Unix()
	result.LockingBalancePolicy = policy
	raw, err := json.Marshal(struct {
		Users            []sourceUser
		Plans            []Plan
		Mappings         map[int64]int64
		Policy           string
		RegenerateTokens bool
		RegenerateUUIDs  bool
	}{snap.users, snap.plans, mappings, policy, regenerate.tokens, regenerate.uuids})
	if err != nil {
		return PreviewResult{}, ErrSourceRead
	}
	digest := sha256.Sum256(raw)
	result.SourceDigest = hex.EncodeToString(digest[:])
	s.mu.Lock()
	stored := len(users)
	for _, existing := range s.previews {
		stored += len(existing.users)
	}
	if stored > 250000 {
		s.mu.Unlock()
		return PreviewResult{}, ErrBusy
	}
	p := &storedPreview{actorID: actorID, result: result, users: users, plans: usedPlans, resetMethod: s.resetMethod(), state: "ready"}
	if !result.CanApply {
		p.users = nil
	}
	s.previews[id] = p
	p.expiryTimer = time.AfterFunc(PreviewTTL, func() { s.expirePreview(id) })
	s.mu.Unlock()
	return result, nil
}

func (s *DBService) expirePreview(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.previews[id]; ok {
		s.expireLocked(id, p)
	}
}
func (s *DBService) expireLocked(id string, p *storedPreview) {
	p.expired = true
	if p.state == "applying" {
		return
	}
	p.users = nil
	if p.expiryTimer != nil {
		p.expiryTimer.Stop()
	}
	// An unknown commit must not become retryable just because its TTL elapsed.
	// Keep only non-sensitive receipt metadata; discard all user hashes/tokens.
	if p.state != "unknown" {
		delete(s.previews, id)
	}
}
func (s *DBService) pruneLocked() {
	now := s.now().Unix()
	for id, p := range s.previews {
		if p.result.ExpiresAt <= now {
			s.expireLocked(id, p)
		}
	}
}

var passwordPattern = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func emailKey(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
func validEmail(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	a, err := mail.ParseAddress(v)
	return err == nil && a.Address == v
}
func validPassword(u sourceUser) bool {
	algo := strings.ToLower(strings.TrimSpace(u.PasswordAlgo))
	if algo != "" && algo != "bcrypt" {
		return false
	}
	if !passwordPattern.MatchString(u.Password) {
		return false
	}
	cost, err := bcrypt.Cost([]byte(u.Password))
	return err == nil && cost >= bcrypt.MinCost && cost <= 16
}
func validValues(u sourceUser, policy string) bool {
	in32 := func(n int64) bool { return n >= math.MinInt32 && n <= math.MaxInt32 }
	if u.ID <= 0 || !in32(u.Balance) || !in32(u.CommissionBalance) || u.LockingBalance < 0 || u.U < 0 || u.D < 0 || u.U > math.MaxInt64-u.D || u.T < 0 || u.T > math.MaxInt32 || u.TransferEnable < 0 || u.CreatedAt < 0 || u.CreatedAt > math.MaxInt32 || u.UpdatedAt < 0 || u.UpdatedAt > math.MaxInt32 {
		return false
	}
	if u.ExpiredAt != nil && *u.ExpiredAt < 0 {
		return false
	}
	if policy == "to_balance" && (u.LockingBalance > math.MaxInt32 || u.Balance > math.MaxInt32-u.LockingBalance) {
		return false
	}
	return true
}

type regenerationOptions struct {
	tokens bool
	uuids  bool
}

func buildPreview(snap snapshot, target targetState, mappings map[int64]int64, policy string, regenerate regenerationOptions) (PreviewResult, []insertUser, map[int64]Plan, error) {
	out := PreviewResult{Counts: Counts{Total: len(snap.users)}, Blockers: []string{}, Warnings: []string{"邮箱去除首尾空白、不区分大小写比对；目标已有账号跳过，密码、余额和权益不变。", "导入的是预览时快照；源站后续变更不会自动包含，需重新预览。", "生成新预览会使当前管理员之前的预览失效。", "保留源用户额度、已用流量和到期时间；后续重置按目标套餐周期，本周期不会立即清零已用流量。", "无套餐用户保留源额度和到期字段，但不分配目标套餐或线路分组，不额外赋予套餐权限。"}}
	out.RegenerateTokens, out.RegenerateUUIDs = regenerate.tokens, regenerate.uuids
	if regenerate.tokens {
		out.Warnings = append(out.Warnings, "已开启强制重新生成订阅 Token：全部新增用户使用新 Token，需重新获取目标站订阅。")
	}
	if regenerate.uuids {
		out.Warnings = append(out.Warnings, "已开启强制重新生成 UUID：全部新增用户使用新 UUID，需重新获取并更新客户端订阅。")
	}
	if regenerate.tokens || regenerate.uuids {
		out.Warnings = append(out.Warnings, "仅更换目标站本次新增用户的选定凭据，不修改源站凭据或用户登录密码；旧站继续运行时，旧凭据仍可能有效。")
	}
	// Generated credentials must not reuse ANY old source credential, including
	// skipped accounts and rows not yet visited. Keep this separate from target
	// collision tracking so the disabled-switch preservation behavior is unchanged.
	sourceTokens, sourceUUIDs := map[string]bool{}, map[string]bool{}
	for _, src := range snap.users {
		sourceTokens[strings.TrimSpace(src.Token)] = true
		sourceUUIDs[strings.ToLower(strings.TrimSpace(src.UUID))] = true
	}
	users := make([]insertUser, 0, len(snap.users))
	plans := map[int64]Plan{}
	seen := map[string]bool{}
	for _, src := range snap.users {
		if src.IsAdmin != 0 || src.IsStaff != 0 {
			out.Counts.SkippedPrivileged++
			continue
		}
		key := emailKey(src.Email)
		if target.emails[key] {
			out.Counts.SkippedExisting++
			continue
		}
		if !validEmail(key) {
			out.Counts.InvalidEmail++
			continue
		}
		if seen[key] {
			out.Counts.SkippedDuplicateEmail++
			continue
		}
		seen[key] = true
		if src.LockingBalance > 0 {
			out.Counts.LockingBalanceUsers++
			if math.MaxInt64-out.Counts.LockingBalanceTotal < src.LockingBalance {
				return PreviewResult{}, nil, nil, ErrSourceSchema
			}
			out.Counts.LockingBalanceTotal += src.LockingBalance
			if policy == "skip" {
				out.Counts.SkippedLockedBalance++
				continue
			}
		}
		var plan *Plan
		if src.PlanID != nil && *src.PlanID > 0 {
			mapped, ok := mappings[*src.PlanID]
			if !ok {
				out.Counts.UnmappedPlan++
				continue
			}
			if mapped == 0 {
				out.Counts.SkippedPlan++
				continue
			}
			p, ok := target.plans[mapped]
			if !ok {
				return PreviewResult{}, nil, nil, ErrInvalidParameter
			}
			plan = &p
			plans[p.ID] = p
		}
		if !validPassword(src) {
			out.Counts.UnsupportedPassword++
			continue
		}
		if !validValues(src, policy) {
			out.Counts.InvalidValues++
			continue
		}
		src.Email = key
		src.PasswordAlgo = ""
		if src.Banned != 0 {
			src.Banned = 1
		}
		if policy == "to_balance" {
			src.Balance += src.LockingBalance
		}
		src.LockingBalance = 0
		if plan == nil {
			out.Counts.NoPlan++
			src.PlanID = nil

		}
		src.Token = strings.TrimSpace(src.Token)
		if regenerate.tokens || !tokenPattern.MatchString(src.Token) || target.tokens[src.Token] {
			var err error
			for {
				src.Token, err = randomHex(16)
				if err != nil {
					return PreviewResult{}, nil, nil, ErrUnavailable
				}
				if !target.tokens[src.Token] && !sourceTokens[src.Token] {
					break
				}
			}
			out.Counts.RegeneratedTokens++
		}
		target.tokens[src.Token] = true
		src.UUID = strings.ToLower(strings.TrimSpace(src.UUID))
		if regenerate.uuids || !uuidPattern.MatchString(src.UUID) || target.uuids[src.UUID] {
			for {
				v, err := randomHex(16)
				if err != nil {
					return PreviewResult{}, nil, nil, ErrUnavailable
				}
				src.UUID = v[:8] + "-" + v[8:12] + "-4" + v[13:16] + "-a" + v[17:20] + "-" + v[20:]
				if !target.uuids[src.UUID] && !sourceUUIDs[src.UUID] {
					break
				}
			}
			out.Counts.RegeneratedUUIDs++
		}
		target.uuids[src.UUID] = true
		users = append(users, insertUser{sourceUser: src, TargetPlan: plan})
	}
	out.Counts.Importable = len(users)
	if out.Counts.InvalidEmail > 0 {
		out.Blockers = append(out.Blockers, "部分源用户邮箱无效或超出目标长度限制，需修正源数据后重新预览。")
	}
	if out.Counts.UnsupportedPassword > 0 {
		out.Blockers = append(out.Blockers, "部分源用户密码不是受支持的 bcrypt 格式，需处理后重新预览。")
	}
	if out.Counts.InvalidValues > 0 {
		out.Blockers = append(out.Blockers, "部分源用户数值超出目标字段范围，需处理后重新预览。")
	}
	if out.Counts.UnmappedPlan > 0 {
		out.Blockers = append(out.Blockers, "部分源用户套餐尚未映射，请选择目标套餐或明确排除该套餐。")
	}
	if len(users) == 0 {
		out.Blockers = append(out.Blockers, "没有可导入的用户。")
	}
	if out.Counts.SkippedDuplicateEmail > 0 {
		out.Warnings = append(out.Warnings, "源站存在重复邮箱，仅保留第一条普通用户记录，其余跳过。")
	}
	if out.Counts.LockingBalanceUsers > 0 && policy == "discard" {
		out.Warnings = append(out.Warnings, "待解锁佣金不迁移；账户余额和已解锁佣金分别保留，不将待解锁金额并入余额。")
	}
	out.CanApply = len(out.Blockers) == 0
	return out, users, plans, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readTargetPlans(ctx context.Context, q queryer) (map[int64]Plan, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, group_id, transfer_enable, device_limit, speed_limit, reset_traffic_method FROM v2_plan ORDER BY id`)
	if err != nil {
		return nil, ErrTargetRead
	}
	defer rows.Close()
	plans := map[int64]Plan{}
	for rows.Next() {
		var p Plan
		if rows.Scan(&p.ID, &p.Name, &p.GroupID, &p.TransferEnable, &p.DeviceLimit, &p.SpeedLimit, &p.ResetTrafficMethod) != nil {
			return nil, ErrTargetRead
		}
		plans[p.ID] = p
		if len(plans) > 10000 {
			return nil, ErrTargetRead
		}
	}
	if rows.Err() != nil {
		return nil, ErrTargetRead
	}
	return plans, nil
}
func sortedPlans(m map[int64]Plan) []Plan {
	out := make([]Plan, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func readTargetState(ctx context.Context, q queryer) (targetState, error) {
	plans, err := readTargetPlans(ctx, q)
	if err != nil {
		return targetState{}, err
	}
	out := targetState{plans: plans, emails: map[string]bool{}, tokens: map[string]bool{}, uuids: map[string]bool{}}
	rows, err := q.QueryContext(ctx, `SELECT email, token, uuid FROM v2_user ORDER BY id LIMIT $1`, maxTargetUsers+1)
	if err != nil {
		return targetState{}, ErrTargetRead
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var email, token, uuid string
		if rows.Scan(&email, &token, &uuid) != nil {
			return targetState{}, ErrTargetRead
		}
		out.emails[emailKey(email)] = true
		out.tokens[strings.TrimSpace(token)] = true
		out.uuids[strings.ToLower(strings.TrimSpace(uuid))] = true
		count++
		if count > maxTargetUsers {
			return targetState{}, ErrTargetRead
		}
	}
	if rows.Err() != nil {
		return targetState{}, ErrTargetRead
	}
	return out, nil
}
func verifyTarget(p *storedPreview, target targetState, resetMethod int64) error {
	if resetMethod != p.resetMethod {
		return ErrPreviewStale
	}
	for id, old := range p.plans {
		current, ok := target.plans[id]
		if !ok || !reflect.DeepEqual(old, current) {
			return ErrPreviewStale
		}
	}
	for _, u := range p.users {
		if target.emails[emailKey(u.Email)] || target.tokens[u.Token] || target.uuids[u.UUID] {
			return ErrPreviewStale
		}
	}
	return nil
}

// String deliberately avoids exposing fields if a caller accidentally formats
// a source configuration in a diagnostic message.
func (c SourceConfig) String() string   { return "SourceConfig{credentials:redacted}" }
func (c SourceConfig) GoString() string { return c.String() }
