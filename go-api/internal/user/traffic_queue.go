package user

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func (s *DBService) QueueTrafficReport(ctx context.Context, report TrafficReport) error {
	if s == nil || (s.db == nil && s.jobs == nil) {
		return ErrUnavailable
	}

	report.ServerType = strings.ToLower(strings.TrimSpace(report.ServerType))
	if report.ServerID <= 0 || report.ServerType == "" || len(report.Traffic) == 0 {
		return nil
	}

	runTraffic := func(jobCtx context.Context) error {
		return s.applyTrafficFetch(jobCtx, report)
	}
	if s.jobs != nil {
		if err := s.jobs.Enqueue("traffic_fetch", "traffic:"+report.ServerType+":"+strconv.FormatInt(report.ServerID, 10), runTraffic); err != nil {
			return err
		}
	} else if err := runTraffic(ctx); err != nil {
		return err
	}

	runStat := func(jobCtx context.Context) error {
		return s.recordTrafficStats(jobCtx, report)
	}
	if s.jobs != nil {
		if err := s.jobs.Enqueue("stat", "stat:"+report.ServerType+":"+strconv.FormatInt(report.ServerID, 10), runStat); err != nil {
			return err
		}
	} else if err := runStat(ctx); err != nil {
		return err
	}

	return nil
}

func (s *DBService) applyTrafficFetch(ctx context.Context, report TrafficReport) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}

	type delta struct {
		userID int64
		u      int64
		d      int64
	}

	changes := make([]delta, 0, len(report.Traffic))
	for userID, usage := range report.Traffic {
		u := scaleTraffic(usage.U, report.ServerRate)
		d := scaleTraffic(usage.D, report.ServerRate)
		if userID <= 0 || (u == 0 && d == 0) {
			continue
		}
		changes = append(changes, delta{userID: userID, u: u, d: d})
	}
	if len(changes) == 0 {
		return nil
	}

	args := make([]any, 0, len(changes)*3+1)
	values := make([]string, 0, len(changes))
	for idx, item := range changes {
		base := idx*3 + 1
		values = append(values, fmt.Sprintf("($%d, $%d, $%d)", base, base+1, base+2))
		args = append(args, item.userID, item.u, item.d)
	}
	now := time.Now().Unix()
	args = append(args, now)

	_, err := s.db.ExecContext(ctx, `UPDATE v2_user AS target
SET u = target.u + delta.u,
	d = target.d + delta.d,
	t = $`+strconv.Itoa(len(args))+`,
	updated_at = $`+strconv.Itoa(len(args))+`
FROM (VALUES `+strings.Join(values, ",")+`) AS delta(id, u, d)
WHERE target.id = delta.id`, args...)
	if err != nil {
		return fmt.Errorf("apply traffic fetch: %w", err)
	}
	return nil
}

func (s *DBService) recordTrafficStats(ctx context.Context, report TrafficReport) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}

	nowTime := time.Now()
	recordAt := time.Date(nowTime.Year(), nowTime.Month(), nowTime.Day(), 0, 0, 0, 0, nowTime.Location()).Unix()
	now := nowTime.Unix()

	serverU := int64(0)
	serverD := int64(0)
	for userID, usage := range report.Traffic {
		if userID <= 0 || (usage.U == 0 && usage.D == 0) {
			continue
		}
		serverU += usage.U
		serverD += usage.D
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_stat_user (
user_id, server_rate, u, d, record_type, record_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, 'd', $5, $6, $6
)
ON CONFLICT (server_rate, user_id, record_at) DO UPDATE SET
u = v2_stat_user.u + EXCLUDED.u,
d = v2_stat_user.d + EXCLUDED.d,
updated_at = EXCLUDED.updated_at`,
			userID, report.ServerRate, usage.U, usage.D, recordAt, now,
		); err != nil {
			return fmt.Errorf("upsert user traffic stat: %w", err)
		}
	}

	if serverU == 0 && serverD == 0 {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_stat_server (
server_id, server_type, u, d, record_type, record_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, 'd', $5, $6, $6
)
ON CONFLICT (server_id, server_type, record_at) DO UPDATE SET
u = v2_stat_server.u + EXCLUDED.u,
d = v2_stat_server.d + EXCLUDED.d,
updated_at = EXCLUDED.updated_at`,
		report.ServerID, report.ServerType, serverU, serverD, recordAt, now,
	)
	if err != nil {
		return fmt.Errorf("upsert server traffic stat: %w", err)
	}
	return nil
}

func scaleTraffic(value int64, rate float64) int64 {
	if value == 0 {
		return 0
	}
	if rate <= 0 {
		rate = 1
	}
	return int64(math.Round(float64(value) * rate))
}
