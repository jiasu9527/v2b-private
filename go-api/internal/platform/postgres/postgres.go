package postgres

import (
	"context"
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, nil
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}
