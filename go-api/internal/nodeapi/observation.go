package nodeapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/platform/postgres"
)

const observationKeepDays = 7
const (
	ObservationModeAllowlist = "allowlist"
	ObservationModeAll       = "all"
)

var (
	ErrObservationUnauthorized   = errors.New("observation node unauthorized or disabled")
	ErrObservationInvalid        = errors.New("invalid observation event")
	ErrObservationUserNotAllowed = errors.New("observation event user is no longer allowed")
	observationEventIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

type ObservationEvent struct {
	EventID       string `json:"event_id"`
	UserID        int64  `json:"user_id"`
	OccurredAt    int64  `json:"occurred_at"`
	ClientIP      string `json:"client_ip"`
	TargetHost    string `json:"target_host"`
	TargetIP      string `json:"target_ip"`
	TargetPort    int    `json:"target_port"`
	Network       string `json:"network"`
	Result        string `json:"result"`
	UplinkBytes   int64  `json:"uplink_bytes"`
	DownlinkBytes int64  `json:"downlink_bytes"`
}

type ObservationReportRequest struct {
	NodeType string
	NodeID   int64
	Token    string
	Events   []ObservationEvent
}

type ObservationFilter struct {
	NodeType string
	NodeID   int64
	UserID   int64
	From     int64
	To       int64
	Page     int
	PageSize int
}

type ObservationNodeSettings struct {
	Configured     bool
	Enabled        bool
	Mode           string
	AllowedUserIDs []int64
}

// ObservationService is optional so existing node API clients remain compatible.
type ObservationService interface {
	ProvisionObservation(ctx context.Context, nodeType string, nodeID int64, enabled bool, mode string, allowedUserIDs []int64) (string, error)
	ObservationNodes(ctx context.Context) ([]map[string]any, error)
	ObservationNodeConfig(ctx context.Context, nodeType string, nodeID int64) (ObservationNodeSettings, error)
	ReportObservation(ctx context.Context, req ObservationReportRequest) (int64, error)
	ObservationEvents(ctx context.Context, filter ObservationFilter) (map[string]any, error)
	ObservationSummary(ctx context.Context, filter ObservationFilter) (map[string]any, error)
	RunObservationMaintenance(ctx context.Context)
}

func (s *DBService) ensureObservationSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("node service unavailable")
	}
	s.observationSchemaOnce.Do(func() {
		s.observationSchemaErr = postgres.EnsureObservationSchema(ctx, s.db)
	})
	return s.observationSchemaErr
}

func validateObservationNode(nodeType string, nodeID int64) error {
	if NormalizeNodeType(nodeType) != "v2node" || nodeID <= 0 {
		return fmt.Errorf("%w: observation requires v2node", ErrObservationInvalid)
	}
	return nil
}

func uniqueObservationUsers(ids []int64, enabled bool) ([]int64, error) {
	if ids == nil || len(ids) > 2000 || (enabled && len(ids) == 0) {
		return nil, fmt.Errorf("%w: enabled observation requires 1..2000 allowed users", ErrObservationInvalid)
	}
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: invalid allowed_user_id", ErrObservationInvalid)
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result, nil
}

// ProvisionObservation rotates the per-node token. The plaintext is returned
// exactly once for installation on the node and is never stored by the panel.
func (s *DBService) ProvisionObservation(ctx context.Context, nodeType string, nodeID int64, enabled bool, mode string, allowedUserIDs []int64) (string, error) {
	if err := validateObservationNode(nodeType, nodeID); err != nil {
		return "", err
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = ObservationModeAllowlist
	}
	if mode != ObservationModeAll && mode != ObservationModeAllowlist {
		return "", fmt.Errorf("%w: unsupported observation mode", ErrObservationInvalid)
	}
	var ids []int64
	if mode == ObservationModeAll {
		if len(allowedUserIDs) != 0 {
			return "", fmt.Errorf("%w: all mode cannot include a user allowlist", ErrObservationInvalid)
		}
		ids = []int64{}
	} else {
		var err error
		ids, err = uniqueObservationUsers(allowedUserIDs, enabled)
		if err != nil {
			return "", err
		}
	}
	if err := s.ensureObservationSchema(ctx); err != nil {
		return "", err
	}
	server, err := s.LookupServer(ctx, ServerLookupRequest{NodeType: "v2node", NodeID: nodeID})
	if err != nil {
		return "", fmt.Errorf("observation node not found: %w", err)
	}
	protocol := strings.ToLower(strings.TrimSpace(fmt.Sprint(server.Fields["protocol"])))
	switch protocol {
	case "vmess", "vless", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls":
	default:
		return "", fmt.Errorf("%w: observation requires an Xray-backed protocol", ErrObservationInvalid)
	}
	var dedicated bool
	if err := s.db.QueryRowContext(ctx, `SELECT client_entry_only=1 FROM v2_server_v2node WHERE id=$1`, nodeID).Scan(&dedicated); err != nil {
		return "", fmt.Errorf("observation node not found: %w", err)
	}
	if mode == ObservationModeAllowlist && !dedicated {
		return "", fmt.Errorf("%w: observation requires a dedicated client_entry_only node", ErrObservationInvalid)
	}
	idsJSON, _ := json.Marshal(ids)
	if mode == ObservationModeAllowlist {
		// Ensure every allowlisted user still exists. Never silently widen the list.
		var existing int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_user WHERE id IN (SELECT value::bigint FROM jsonb_array_elements_text($1::jsonb))`, string(idsJSON)).Scan(&existing); err != nil {
			return "", err
		}
		if existing != len(ids) {
			return "", fmt.Errorf("%w: allowed users include deleted accounts", ErrObservationInvalid)
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	token := hex.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO v2_node_observation_config(node_type,node_id,enabled,mode,token_hash,allowed_user_ids,created_at,updated_at)
VALUES('v2node',$1,$2,$3,$4,$5::jsonb,$6,$6)
ON CONFLICT(node_type,node_id) DO UPDATE SET enabled=EXCLUDED.enabled,mode=EXCLUDED.mode,token_hash=EXCLUDED.token_hash,allowed_user_ids=EXCLUDED.allowed_user_ids,updated_at=EXCLUDED.updated_at`, nodeID, enabled, mode, hash[:], string(idsJSON), now)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *DBService) ObservationNodeConfig(ctx context.Context, nodeType string, nodeID int64) (ObservationNodeSettings, error) {
	if err := validateObservationNode(nodeType, nodeID); err != nil {
		return ObservationNodeSettings{}, err
	}
	if err := s.ensureObservationSchema(ctx); err != nil {
		return ObservationNodeSettings{}, err
	}
	var settings ObservationNodeSettings
	var idsJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT enabled,mode,allowed_user_ids FROM v2_node_observation_config WHERE node_type='v2node' AND node_id=$1`, nodeID).Scan(&settings.Enabled, &settings.Mode, &idsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ObservationNodeSettings{}, nil
	}
	if err != nil {
		return ObservationNodeSettings{}, err
	}
	settings.Configured = true
	if settings.Mode != ObservationModeAll && settings.Mode != ObservationModeAllowlist {
		return ObservationNodeSettings{}, fmt.Errorf("%w: stored observation mode", ErrObservationInvalid)
	}
	if err := json.Unmarshal(idsJSON, &settings.AllowedUserIDs); err != nil {
		return ObservationNodeSettings{}, err
	}
	if settings.Mode == ObservationModeAll && len(settings.AllowedUserIDs) != 0 {
		return ObservationNodeSettings{}, fmt.Errorf("%w: all mode has user allowlist", ErrObservationInvalid)
	}
	return settings, nil
}

func (s *DBService) ObservationNodes(ctx context.Context) ([]map[string]any, error) {
	if err := s.ensureObservationSchema(ctx); err != nil {
		return nil, err
	}
	// Include unconfigured nodes: an all-user mode on just one node must not
	// look like full-site coverage to the administrator.
	rows, err := s.db.QueryContext(ctx, `SELECT n.id,n.name,n.protocol,n.client_entry_only,n."show",
 c.node_id IS NOT NULL,COALESCE(c.enabled,false),COALESCE(c.mode,''),
 COALESCE(c.allowed_user_ids,'[]'::jsonb),COALESCE(c.created_at,0),COALESCE(c.updated_at,0),
 COALESCE(latest.occurred_at,0)
FROM v2_server_v2node n
LEFT JOIN v2_node_observation_config c ON c.node_type='v2node' AND c.node_id=n.id
LEFT JOIN LATERAL (
 SELECT occurred_at FROM v2_node_observation_event
 WHERE node_type='v2node' AND node_id=n.id ORDER BY occurred_at DESC,id DESC LIMIT 1
) latest ON TRUE
ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		var name, protocol, mode string
		var nodeID, createdAt, updatedAt, lastEventAt int64
		var clientEntryOnly, show int
		var configured, enabled bool
		var idsJSON []byte
		if err := rows.Scan(&nodeID, &name, &protocol, &clientEntryOnly, &show, &configured, &enabled, &mode, &idsJSON, &createdAt, &updatedAt, &lastEventAt); err != nil {
			return nil, err
		}
		var ids []int64
		if err := json.Unmarshal(idsJSON, &ids); err != nil {
			return nil, err
		}
		protocol = strings.ToLower(strings.TrimSpace(protocol))
		supported := false
		switch protocol {
		case "vmess", "vless", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls":
			supported = true
		}
		result = append(result, map[string]any{
			"node_type": "v2node", "node_id": nodeID, "name": name, "protocol": protocol,
			"show": show, "client_entry_only": clientEntryOnly == 1, "supported": supported,
			"configured": configured, "enabled": enabled, "mode": mode,
			"allowed_user_ids": ids, "created_at": createdAt, "updated_at": updatedAt,
			"last_event_at": lastEventAt,
		})
	}
	return result, rows.Err()
}

func normalizeObservationEvent(event *ObservationEvent, now int64) error {
	event.EventID = strings.TrimSpace(event.EventID)
	event.ClientIP = strings.TrimSpace(event.ClientIP)
	event.TargetHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(event.TargetHost), "."))
	event.TargetIP = strings.TrimSpace(event.TargetIP)
	event.Network = strings.ToLower(strings.TrimSpace(event.Network))
	event.Result = strings.ToLower(strings.TrimSpace(event.Result))
	if !observationEventIDPattern.MatchString(event.EventID) || event.UserID <= 0 || event.OccurredAt < now-observationKeepDays*86400 || event.OccurredAt > now+300 || event.TargetPort < 1 || event.TargetPort > 65535 || event.UplinkBytes < 0 || event.DownlinkBytes < 0 {
		return ErrObservationInvalid
	}
	// This first-stage collector captures routing attempts only. Counts of bytes
	// or a "connected" result would imply verification the node cannot provide.
	if event.UplinkBytes != 0 || event.DownlinkBytes != 0 || event.Result != "route_attempt" {
		return ErrObservationInvalid
	}
	if event.ClientIP != "" && net.ParseIP(event.ClientIP) == nil {
		return ErrObservationInvalid
	}
	if event.TargetIP != "" && net.ParseIP(event.TargetIP) == nil {
		return ErrObservationInvalid
	}
	if event.TargetHost != "" {
		if len(event.TargetHost) > 253 || strings.ContainsAny(event.TargetHost, "/:@?# \\\t\r\n") || strings.Contains(event.TargetHost, "..") {
			return ErrObservationInvalid
		}
		if ip := net.ParseIP(event.TargetHost); ip != nil {
			event.TargetIP = ip.String()
			event.TargetHost = ""
		}
	}
	if event.TargetHost == "" && event.TargetIP == "" {
		return ErrObservationInvalid
	}
	if event.Network != "tcp" && event.Network != "udp" {
		return ErrObservationInvalid
	}
	return nil
}

func (s *DBService) ReportObservation(ctx context.Context, req ObservationReportRequest) (int64, error) {
	if err := validateObservationNode(req.NodeType, req.NodeID); err != nil {
		return 0, err
	}
	if len(req.Events) == 0 || len(req.Events) > 100 || len(req.Token) != 64 {
		return 0, ErrObservationInvalid
	}
	if err := s.ensureObservationSchema(ctx); err != nil {
		return 0, err
	}
	var enabled bool
	var mode string
	var storedHash, idsJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT enabled,mode,token_hash,allowed_user_ids FROM v2_node_observation_config WHERE node_type='v2node' AND node_id=$1`, req.NodeID).Scan(&enabled, &mode, &storedHash, &idsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrObservationUnauthorized
	}
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256([]byte(req.Token))
	if !enabled || subtle.ConstantTimeCompare(storedHash, hash[:]) != 1 {
		return 0, ErrObservationUnauthorized
	}
	if mode != ObservationModeAll && mode != ObservationModeAllowlist {
		return 0, ErrObservationUnauthorized
	}
	var nodeExists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_server_v2node WHERE id=$1 AND ($2='all' OR client_entry_only=1))`, req.NodeID, mode).Scan(&nodeExists); err != nil {
		return 0, err
	}
	if !nodeExists {
		return 0, ErrObservationUnauthorized
	}
	var allowed map[int64]struct{}
	if mode == ObservationModeAllowlist {
		var ids []int64
		if err := json.Unmarshal(idsJSON, &ids); err != nil {
			return 0, err
		}
		allowed = make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			allowed[id] = struct{}{}
		}
	}
	now := time.Now().Unix()
	for i := range req.Events {
		if err := normalizeObservationEvent(&req.Events[i], now); err != nil {
			return 0, fmt.Errorf("%w: event %d", err, i)
		}
		if mode == ObservationModeAllowlist {
			if _, ok := allowed[req.Events[i].UserID]; !ok {
				// This is an invalid *event*, not a bad node token. Reporting it as
				// invalid lets the node isolate a stale queued event after an
				// allowlist change without blocking all later events.
				return 0, ErrObservationUserNotAllowed
			}
		}
	}
	if mode == ObservationModeAll {
		uniqueIDs := make(map[int64]struct{}, len(req.Events))
		ids := make([]int64, 0, len(req.Events))
		for _, event := range req.Events {
			if _, seen := uniqueIDs[event.UserID]; !seen {
				uniqueIDs[event.UserID] = struct{}{}
				ids = append(ids, event.UserID)
			}
		}
		var existing int
		idsJSON, _ := json.Marshal(ids)
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_user WHERE id IN (SELECT value::bigint FROM jsonb_array_elements_text($1::jsonb))`, string(idsJSON)).Scan(&existing); err != nil {
			return 0, err
		}
		if existing != len(ids) {
			return 0, ErrObservationUserNotAllowed
		}
	}
	// One atomic statement per batch avoids up to 100 round trips for every
	// node report when all-user collection is enabled.
	var sqlText strings.Builder
	sqlText.WriteString(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,client_ip,target_host,target_ip,target_port,network,result,uplink_bytes,downlink_bytes,received_at) VALUES `)
	args := make([]any, 0, len(req.Events)*13)
	for i, event := range req.Events {
		if i > 0 {
			sqlText.WriteByte(',')
		}
		sqlText.WriteString("('v2node'")
		for field := 0; field < 13; field++ {
			sqlText.WriteString(",$")
			sqlText.WriteString(strconv.Itoa(len(args) + field + 1))
		}
		sqlText.WriteByte(')')
		args = append(args, req.NodeID, event.EventID, event.UserID, event.OccurredAt, event.ClientIP, event.TargetHost, event.TargetIP, event.TargetPort, event.Network, event.Result, event.UplinkBytes, event.DownlinkBytes, now)
	}
	sqlText.WriteString(` ON CONFLICT(node_type,node_id,event_id) DO NOTHING`)
	result, err := s.db.ExecContext(ctx, sqlText.String(), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *DBService) RunObservationMaintenance(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = s.DeleteExpiredObservation(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *DBService) DeleteExpiredObservation(ctx context.Context) error {
	if err := s.ensureObservationSchema(ctx); err != nil {
		return err
	}
	// Bound each delete transaction so large all-user logs do not lock or
	// generate WAL for the entire seven-day backlog at once.
	_, err := s.db.ExecContext(ctx, `WITH expired AS (
SELECT id FROM v2_node_observation_event WHERE occurred_at < $1 ORDER BY occurred_at,id LIMIT 50000
)
DELETE FROM v2_node_observation_event WHERE id IN (SELECT id FROM expired)`, time.Now().Unix()-observationKeepDays*86400)
	return err
}
