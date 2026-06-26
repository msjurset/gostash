package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

func (s *SQLiteStore) ApproveFailover(ctx context.Context, operation string, expiresAt time.Time) error {
	q := `
		INSERT INTO ai_failover_approvals (operation, approved_at, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(operation) DO UPDATE SET
			approved_at = excluded.approved_at,
			expires_at = excluded.expires_at
	`
	_, err := s.db.ExecContext(ctx, q, operation, time.Now().UTC(), expiresAt.UTC())
	return err
}

func (s *SQLiteStore) IsFailoverApproved(ctx context.Context, operation string) (bool, error) {
	q := `SELECT expires_at FROM ai_failover_approvals WHERE operation = ?`
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, q, operation).Scan(&expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return time.Now().UTC().Before(expiresAt), nil
}

func (s *SQLiteStore) RegisterUsageLogs(ctx context.Context, logs []model.UsageLog) ([]model.UsageLog, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO synced_usage_logs (id, model, prompt_tokens, candidate_tokens, created_at)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	var newlyRegistered []model.UsageLog
	for _, log := range logs {
		res, err := stmt.ExecContext(ctx, log.ID, log.Model, log.PromptTokens, log.CandidateTokens, log.CreatedAt.UTC())
		if err != nil {
			return nil, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows > 0 {
			newlyRegistered = append(newlyRegistered, log)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return newlyRegistered, nil
}
