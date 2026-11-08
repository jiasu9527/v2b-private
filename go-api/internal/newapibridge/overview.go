package newapibridge

import (
	"context"
	"time"
)

type Binding struct {
	UserID       int64  `json:"user_id"`
	Email        string `json:"email"`
	RemoteUserID int64  `json:"remote_user_id"`
	CreatedAt    int64  `json:"created_at"`
}
type BindingPage struct {
	Items    []Binding `json:"items"`
	Total    int64     `json:"total"`
	Current  int       `json:"current"`
	PageSize int       `json:"page_size"`
}

func (s *Service) ListBindings(ctx context.Context, current, size int) (BindingPage, error) {
	if current < 1 || current > 100000 || size < 1 || size > 100 {
		return BindingPage{}, ErrInvalid
	}
	if e := s.EnsureSchema(ctx); e != nil {
		return BindingPage{}, e
	}
	c, e := s.loadSettings(ctx)
	if e != nil {
		return BindingPage{}, e
	}
	p := BindingPage{Items: []Binding{}, Current: current, PageSize: size}
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT user_id,remote_user_id FROM v2_newapi_checkout WHERE api_url=$1 GROUP BY user_id,remote_user_id) b`, c.APIURL).Scan(&p.Total); e != nil {
		return p, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT b.user_id,COALESCE(u.email,''),b.remote_user_id,b.created_at FROM (SELECT user_id,remote_user_id,MIN(created_at) AS created_at FROM v2_newapi_checkout WHERE api_url=$1 GROUP BY user_id,remote_user_id) b LEFT JOIN v2_user u ON u.id=b.user_id ORDER BY b.created_at DESC,b.user_id LIMIT $2 OFFSET $3`, c.APIURL, size, (current-1)*size)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var b Binding
		if e = rows.Scan(&b.UserID, &b.Email, &b.RemoteUserID, &b.CreatedAt); e != nil {
			return p, e
		}
		p.Items = append(p.Items, b)
	}
	return p, rows.Err()
}

func (s *Service) Overview(ctx context.Context, id int64) (map[string]any, error) {
	info, e := s.PublicInfo(ctx)
	if e != nil {
		return nil, e
	}
	user, e := s.userIdentity(ctx, id)
	if e != nil {
		return nil, e
	}
	var balance, spent int64
	if e = s.db.QueryRowContext(ctx, `SELECT balance FROM v2_user WHERE id=$1`, id).Scan(&balance); e != nil {
		return nil, e
	}
	// Limits use a rolling 24-hour window, not the application server timezone.
	if e = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_fen),0) FROM v2_newapi_transfer WHERE user_id=$1 AND created_at>=$2`, id, time.Now().Add(-24*time.Hour).Unix()).Scan(&spent); e != nil {
		return nil, e
	}
	info["user_id"] = id
	info["email"] = user.Email
	info["balance_fen"] = balance
	info["transferred_24h_fen"] = spent
	return info, nil
}
