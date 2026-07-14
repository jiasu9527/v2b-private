package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const dnsFailoverDiagnosticLogTimeout = 2 * time.Second

type DNSFailoverLogRecord struct {
	ID        int64  `json:"id"`
	GroupID   int64  `json:"group_id"`
	ProbeID   *int64 `json:"probe_id"`
	TargetID  *int64 `json:"target_id"`
	Stage     string `json:"stage"`
	Level     string `json:"level"`
	Outcome   string `json:"outcome"`
	Message   string `json:"message"`
	Details   string `json:"details"`
	CreatedAt int64  `json:"created_at"`
}

type DNSFailoverLogListRequest struct {
	GroupID  *int64
	ProbeID  *int64
	TargetID *int64
	Stage    string
	Level    string
	Outcome  string
	Current  int64
	PageSize int64
}

type DNSFailoverLogListResult struct {
	Data     []DNSFailoverLogRecord `json:"data"`
	Total    int64                  `json:"total"`
	Current  int64                  `json:"current"`
	PageSize int64                  `json:"page_size"`
}

type dnsFailoverLogEntry struct {
	GroupID   int64  `json:"group_id"`
	ProbeID   *int64 `json:"probe_id"`
	TargetID  *int64 `json:"target_id"`
	Stage     string `json:"stage"`
	Level     string `json:"level"`
	Outcome   string `json:"outcome"`
	Message   string `json:"message"`
	Details   any    `json:"details"`
	CreatedAt int64  `json:"created_at"`
}

type dnsFailoverLogExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertDNSFailoverLogs(ctx context.Context, execer dnsFailoverLogExecer, entries []dnsFailoverLogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode DNS failover logs: %w", err)
	}
	_, err = execer.ExecContext(ctx, `INSERT INTO v2_dns_failover_log (
group_id, probe_id, target_id, stage, level, outcome, message, details, created_at
) SELECT requested.group_id, requested.probe_id, requested.target_id,
requested.stage, requested.level, requested.outcome, requested.message,
COALESCE(requested.details, '{}'::jsonb)::text, requested.created_at
FROM jsonb_to_recordset($1::jsonb) AS requested(
  group_id bigint, probe_id bigint, target_id bigint, stage text, level text,
  outcome text, message text, details jsonb, created_at bigint
)`, string(encoded))
	if err != nil {
		return fmt.Errorf("insert DNS failover logs: %w", err)
	}
	return nil
}

// writeDNSFailoverLogsBestEffort deliberately runs outside the state-changing
// transaction. Diagnostic storage must never roll back an accepted probe
// result or a completed DNS mutation.
func (s *DBService) writeDNSFailoverLogsBestEffort(ctx context.Context, entries ...dnsFailoverLogEntry) {
	if s == nil || s.dnsFailoverLogWriter == nil || len(entries) == 0 {
		return
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dnsFailoverDiagnosticLogTimeout)
	defer cancel()
	if err := s.dnsFailoverLogWriter(logCtx, entries); err != nil {
		s.logDNSFailoverWorkerError("DNS failover diagnostic log write failed: %v", err)
	}
}

func (s *DBService) ListDNSFailoverLogs(ctx context.Context, request DNSFailoverLogListRequest) (DNSFailoverLogListResult, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSFailoverLogListResult{}, err
	}
	current, pageSize := normalizeDNSFailoverEventPage(request.Current, request.PageSize)
	conditions := []string{}
	args := []any{}
	addID := func(column string, value *int64) error {
		if value == nil {
			return nil
		}
		if *value <= 0 {
			return errors.New("日志筛选 ID 无效")
		}
		args = append(args, *value)
		conditions = append(conditions, fmt.Sprintf("%s = $%d", column, len(args)))
		return nil
	}
	if err := addID("group_id", request.GroupID); err != nil {
		return DNSFailoverLogListResult{}, err
	}
	if err := addID("probe_id", request.ProbeID); err != nil {
		return DNSFailoverLogListResult{}, err
	}
	if err := addID("target_id", request.TargetID); err != nil {
		return DNSFailoverLogListResult{}, err
	}
	for _, field := range []struct {
		column string
		value  string
	}{{"stage", request.Stage}, {"level", request.Level}, {"outcome", request.Outcome}} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		if err := validateDNSFailoverIdentifierText("日志筛选", value); err != nil {
			return DNSFailoverLogListResult{}, err
		}
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("%s = $%d", field.column, len(args)))
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	result := DNSFailoverLogListResult{Data: []DNSFailoverLogRecord{}, Current: current, PageSize: pageSize}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_dns_failover_log`+where, args...).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("读取故障转移日志总数失败: %w", err)
	}
	queryArgs := append([]any(nil), args...)
	queryArgs = append(queryArgs, pageSize, (current-1)*pageSize)
	query := `SELECT id, group_id, probe_id, target_id, stage, level, outcome, message, details, created_at
FROM v2_dns_failover_log` + where + fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(queryArgs)-1, len(queryArgs))
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return result, fmt.Errorf("读取故障转移日志失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row DNSFailoverLogRecord
		var probeID, targetID sql.NullInt64
		if err := rows.Scan(&row.ID, &row.GroupID, &probeID, &targetID, &row.Stage, &row.Level, &row.Outcome, &row.Message, &row.Details, &row.CreatedAt); err != nil {
			return result, err
		}
		row.ProbeID = dnsNullInt64Pointer(probeID)
		row.TargetID = dnsNullInt64Pointer(targetID)
		result.Data = append(result.Data, row)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, nil
}
