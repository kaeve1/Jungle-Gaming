package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
)

func (r *repositories) FindInbox(ctx context.Context, consumer, id string) (string, error) {
	var hash string
	err := r.tx.QueryRow(ctx, `SELECT message_hash FROM wallet.inbox_messages WHERE consumer_name=$1 AND message_id=$2`, consumer, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return hash, err
}
func (r *repositories) InsertInbox(ctx context.Context, consumer, id, hash string, tx domain.ID) error {
	tag, err := r.tx.Exec(ctx, `INSERT INTO wallet.inbox_messages (consumer_name,message_id,message_hash,transaction_id,received_at,processed_at)
 VALUES ($1,$2,$3,$4,transaction_timestamp(),clock_timestamp()) ON CONFLICT DO NOTHING`, consumer, id, hash, tx.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return application.ErrConcurrentDuplicate
	}
	return nil
}
