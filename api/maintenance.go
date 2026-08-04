package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func startSecurityMaintenance(ctx context.Context, db *pgxpool.Pool, logger *slog.Logger) {
	run := func() {
		maintenanceCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		tx, err := db.Begin(maintenanceCtx)
		if err == nil {
			defer tx.Rollback(maintenanceCtx)
			_, err = tx.Exec(maintenanceCtx, `SET LOCAL statement_timeout='2s'`)
			if err == nil {
				_, err = tx.Exec(maintenanceCtx, `SELECT app.cleanup_rate_limits()`)
			}
			if err == nil {
				err = tx.Commit(maintenanceCtx)
			}
		}
		if err != nil && ctx.Err() == nil {
			logger.WarnContext(ctx, "security maintenance deferred", "category", "database_unavailable")
		}
	}
	go func() {
		run()
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	go func() {
		maintenanceCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, _ = db.Exec(maintenanceCtx, `UPDATE app.invitation_tokens SET delivery_state='unknown',delivery_category='interrupted' WHERE delivery_state='pending' AND issued_at < transaction_timestamp()-interval '30 seconds'`)
		_, _ = db.Exec(maintenanceCtx, `UPDATE app.password_resets SET delivery_state='unknown',delivery_category='interrupted' WHERE delivery_state='pending' AND issued_at < transaction_timestamp()-interval '30 seconds'`)
	}()
}
