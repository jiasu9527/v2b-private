package nodeapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/user"
)

type TrafficUsage struct {
	U int64
	D int64
}

type TrafficPushRequest struct {
	NodeID   int64
	NodeType string
	Traffic  map[int64]TrafficUsage
}

type ServerLookupRequest struct {
	NodeID   int64
	NodeType string
}

type ServerRecord struct {
	ID       int64
	NodeType string
	GroupIDs []int64
	RouteIDs []int64
	Fields   map[string]any
}

type AvailableUser struct {
	ID          int64  `json:"id" msgpack:"id"`
	UUID        string `json:"uuid,omitempty" msgpack:"uuid,omitempty"`
	SpeedLimit  *int64 `json:"speed_limit,omitempty" msgpack:"speed_limit,omitempty"`
	DeviceLimit *int64 `json:"device_limit,omitempty" msgpack:"device_limit,omitempty"`
}

type AliveReportRequest struct {
	NodeID   int64
	NodeType string
	Users    map[int64][]string
}

type Service interface {
	PushTraffic(ctx context.Context, req TrafficPushRequest) error
	LookupServer(ctx context.Context, req ServerLookupRequest) (ServerRecord, error)
	TouchLastCheck(ctx context.Context, nodeType string, nodeID int64) error
	AvailableUsers(ctx context.Context, groupIDs []int64) ([]AvailableUser, error)
	Routes(ctx context.Context, routeIDs []int64) ([]map[string]any, error)
	AliveList(ctx context.Context) (map[int64]int64, error)
	ReportAlive(ctx context.Context, req AliveReportRequest) error
}

type trafficReporter interface {
	QueueTrafficReport(ctx context.Context, report user.TrafficReport) error
}

type runtimeConfigProvider interface {
	CurrentConfig() config.Config
}

type DBService struct {
	cfg      config.Config
	db       *sql.DB
	reporter trafficReporter
	runtime  runtimeConfigProvider
}

var nodeServerTables = map[string]string{
	"shadowsocks": "v2_server_shadowsocks",
	"vmess":       "v2_server_vmess",
	"trojan":      "v2_server_trojan",
	"tuic":        "v2_server_tuic",
	"hysteria":    "v2_server_hysteria",
	"vless":       "v2_server_vless",
	"anytls":      "v2_server_anytls",
	"v2node":      "v2_server_v2node",
}

func NewDBService(cfg config.Config, db *sql.DB, reporter trafficReporter) *DBService {
	return &DBService{cfg: cfg, db: db, reporter: reporter}
}

func (s *DBService) WithRuntimeConfig(runtime runtimeConfigProvider) *DBService {
	s.runtime = runtime
	return s
}

func (s *DBService) currentConfig() config.Config {
	if s == nil {
		return config.Config{}
	}
	if s.runtime == nil {
		return s.cfg
	}
	return s.runtime.CurrentConfig()
}

func (s *DBService) PushTraffic(ctx context.Context, req TrafficPushRequest) error {
	if s == nil || s.db == nil || s.reporter == nil {
		return errors.New("node service unavailable")
	}

	req.NodeType = NormalizeNodeType(req.NodeType)
	if req.NodeID <= 0 || req.NodeType == "" {
		return errors.New("invalid node target")
	}
	if len(req.Traffic) == 0 {
		return nil
	}

	rate, err := s.loadNodeRate(ctx, req.NodeType, req.NodeID)
	if err != nil {
		return err
	}

	if err := s.reporter.QueueTrafficReport(ctx, user.TrafficReport{
		ServerID:   req.NodeID,
		ServerType: req.NodeType,
		ServerRate: rate,
		Traffic:    convertTraffic(req.Traffic),
	}); err != nil {
		return err
	}

	now := time.Now().Unix()
	if err := s.setRuntimeKV(ctx, serverRuntimeKey(req.NodeType, "LAST_PUSH_AT", req.NodeID), strconv.FormatInt(now, 10), 3600); err != nil {
		return err
	}
	if err := s.setRuntimeKV(ctx, serverRuntimeKey(req.NodeType, "ONLINE_USER", req.NodeID), strconv.Itoa(len(req.Traffic)), 3600); err != nil {
		return err
	}
	return nil
}

func NormalizeNodeType(nodeType string) string {
	switch strings.ToLower(strings.TrimSpace(nodeType)) {
	case "v2ray":
		return "vmess"
	case "hysteria2":
		return "hysteria"
	default:
		return strings.ToLower(strings.TrimSpace(nodeType))
	}
}

func convertTraffic(input map[int64]TrafficUsage) map[int64]user.TrafficUsage {
	result := make(map[int64]user.TrafficUsage, len(input))
	for userID, usage := range input {
		result[userID] = user.TrafficUsage{U: usage.U, D: usage.D}
	}
	return result
}

func (s *DBService) loadNodeRate(ctx context.Context, nodeType string, nodeID int64) (float64, error) {
	table, ok := nodeServerTables[nodeType]
	if !ok {
		return 0, errors.New("unsupported node type")
	}

	var rate float64
	err := s.db.QueryRowContext(ctx, `SELECT CAST(rate AS double precision) FROM `+table+` WHERE id = $1 LIMIT 1`, nodeID).Scan(&rate)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("server is not exist")
		}
		return 0, fmt.Errorf("query node rate: %w", err)
	}
	if rate <= 0 {
		rate = 1
	}
	return rate, nil
}

func (s *DBService) setRuntimeKV(ctx context.Context, key, value string, ttlSeconds int64) error {
	now := time.Now().Unix()
	expireAt := int64(0)
	if ttlSeconds > 0 {
		expireAt = now + ttlSeconds
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`, key, value, expireAt, now)
	if err != nil {
		return fmt.Errorf("set runtime kv: %w", err)
	}
	return nil
}

func serverRuntimeKey(nodeType, suffix string, nodeID int64) string {
	return "SERVER_" + strings.ToUpper(nodeType) + "_" + suffix + "_" + strconv.FormatInt(nodeID, 10)
}
