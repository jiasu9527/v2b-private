package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// claimHealthyEntryEvidenceBackupIPs retains the existing pool health and
// ownership guards, adding an exact port match before LIMIT. Evidence-mode
// splits preserve the delivered node port, so probing another port is not proof
// of suitability. The legacy allocator remains unchanged for existing callers.
func claimHealthyEntryEvidenceBackupIPs(ctx context.Context, tx *sql.Tx, count, port, now int64) ([]ClientEntryBackupIPRecord, error) {
	if tx == nil || count <= 0 || count > clientEntryBackupIPMaxBatch || port < 1 || port > 65535 {
		return nil, errors.New("备用 IP 领取参数无效")
	}
	rows, err := tx.QueryContext(ctx, `SELECT backup.id, backup.name, backup.ip, backup.port,
backup.enabled, backup.check_interval_sec, backup.tcp_timeout_ms, backup.generation,
backup.sort, backup.quarantine_until, backup.created_at, backup.updated_at
FROM v2_client_entry_backup_ip backup
WHERE backup.enabled = 1
  AND backup.port = $5
  AND backup.quarantine_until <= $1
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy policy
	WHERE btrim(policy.entry_host) = backup.ip
  )
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group split_group
	WHERE btrim(split_group.entry_host) = backup.ip
  )
  AND EXISTS (
	SELECT 1 FROM v2_dns_probe probe
	WHERE probe.enabled = 1
	  AND probe.last_heartbeat_at IS NOT NULL
	  AND probe.last_heartbeat_at BETWEEN $1 - $2 AND $1
  )
  AND NOT EXISTS (
	SELECT 1 FROM v2_dns_probe probe
	WHERE probe.enabled = 1
	  AND probe.last_heartbeat_at IS NOT NULL
	  AND probe.last_heartbeat_at BETWEEN $1 - $2 AND $1
	  AND NOT EXISTS (
		SELECT 1 FROM v2_client_entry_backup_ip_state state
		WHERE state.backup_ip_id = backup.id AND state.probe_id = probe.id
		  AND state.last_success = 1
		  AND state.consecutive_success >= $3
		  AND state.last_reported_at IS NOT NULL
		  AND state.last_reported_at >= $1 - LEAST($2,
		      2 * (backup.check_interval_sec::BIGINT + (backup.tcp_timeout_ms::BIGINT + 999) / 1000))
	  )
  )
ORDER BY backup.sort ASC, backup.id ASC
LIMIT $4
FOR UPDATE OF backup SKIP LOCKED`, now, defaultProbeOfflineSec, clientEntryBackupIPSuccessThreshold, count, port)
	if err != nil {
		return nil, fmt.Errorf("领取健康备用 IP 失败: %w", err)
	}
	defer rows.Close()
	result := make([]ClientEntryBackupIPRecord, 0, count)
	for rows.Next() {
		var item ClientEntryBackupIPRecord
		var enabled int64
		if err := rows.Scan(&item.ID, &item.Name, &item.IP, &item.Port, &enabled,
			&item.CheckIntervalSec, &item.TCPTimeoutMS, &item.Generation, &item.Sort,
			&item.QuarantineUntil, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("读取健康备用 IP 失败: %w", err)
		}
		item.Enabled = enabled == 1
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历健康备用 IP 失败: %w", err)
	}
	if int64(len(result)) != count {
		return nil, fmt.Errorf("%w，需要端口 %d 的健康备用 IP %d 个，当前可分配 %d 个", ErrClientEntryBackupIPInsufficient, port, count, len(result))
	}
	return result, nil
}
