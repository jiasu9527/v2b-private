package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Routing already owns the topology lock. Workers acquire probe/monitor locks
// before topology, so attempting a blocking monitor lock here would invert that
// order. All existing-row locks are NOWAIT inside a savepoint: contention only
// defers monitor setup, never blocks the worker or rolls back an otherwise valid
// split. The fallback is durably reported with the execution, not silent success.
func inheritEntryEvidenceMonitor(ctx context.Context, tx *sql.Tx, c EntryEvidenceCase, execution EntryEvidenceExecution, now int64) (string, string, error) {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT entry_evidence_monitor`); err != nil {
		return "", "", err
	}
	status, err := copyEntryEvidenceMonitor(ctx, tx, c, execution, now)
	if err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT entry_evidence_monitor`); rollbackErr != nil {
			return "", "", rollbackErr
		}
		if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT entry_evidence_monitor`); releaseErr != nil {
			return "", "", releaseErr
		}
		return "deferred", "本次未继承入口检测配置（配置争用或校验失败）；路由二分已完成，新规则需在入口检测中手动启用，未检测不等于正常。", nil
	}
	if _, err = tx.ExecContext(ctx, `RELEASE SAVEPOINT entry_evidence_monitor`); err != nil {
		return "", "", err
	}
	if status == "unconfigured" {
		return status, "来源规则未启用入口检测，新规则不会自动开启检测；如需比较A/B健康状态，请在入口检测中启用新规则。", nil
	}
	return status, "", nil
}

func copyEntryEvidenceMonitor(ctx context.Context, tx *sql.Tx, c EntryEvidenceCase, execution EntryEvidenceExecution, now int64) (string, error) {
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id=1 FOR UPDATE NOWAIT`).Scan(&revision); err != nil {
		return "", err
	}
	var sourceID int64
	var interval, timeout, failures, successes int64
	err := tx.QueryRowContext(ctx, `SELECT id,check_interval_sec,tcp_timeout_ms,failure_threshold,success_threshold FROM v2_client_entry_monitor WHERE policy_id=$1 AND enabled=1 FOR KEY SHARE NOWAIT`, c.Target.PolicyID).Scan(&sourceID, &interval, &timeout, &failures, &successes)
	if errors.Is(err, sql.ErrNoRows) {
		return "unconfigured", nil
	}
	if err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id FROM v2_dns_probe p JOIN v2_client_entry_monitor_probe binding ON binding.probe_id=p.id WHERE binding.monitor_id=$1 ORDER BY p.id FOR KEY SHARE OF p NOWAIT`, sourceID)
	if err != nil {
		return "", err
	}
	probes := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		probes = append(probes, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	var monitorID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_monitor(policy_id,enabled,check_interval_sec,tcp_timeout_ms,failure_threshold,success_threshold,created_at,updated_at) VALUES($1,1,$2,$3,$4,$5,$6,$6) RETURNING id`, execution.PolicyID, interval, timeout, failures, successes, now).Scan(&monitorID)
	if err != nil {
		return "", err
	}
	for _, probeID := range probes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_probe(monitor_id,probe_id,created_at) VALUES($1,$2,$3)`, monitorID, probeID, now); err != nil {
			return "", err
		}
	}
	for index, target := range []struct {
		group int64
		host  string
		name  string
	}{{execution.GroupAID, execution.EntryHostA, "A"}, {execution.GroupBID, execution.EntryHostB, "B"}} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_target(monitor_id,source_key,name,host,port,sort,auto_split_enabled,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,0,$7,$7)`, monitorID, fmt.Sprintf("policy:%d:split-group:%d", execution.PolicyID, target.group), target.name, target.host, c.Query.Port, (index+1)*10, now); err != nil {
			return "", err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_config SET revision=revision+1,updated_at=$1 WHERE id=1`, now); err != nil {
		return "", err
	}
	return "inherited", nil
}
