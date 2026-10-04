package application

import (
	"context"
	"time"
)

// OutboxRecord contains the immutable envelope captured by the financial commit.
type OutboxRecord struct {
	EventID, PartitionKey, Token string
	MessageID                    string
	Payload                      []byte
	Attempts                     int
}

// Claim reserves exactly one immediately available sending slot. All updates
// require the current token and an unexpired lease measured by PostgreSQL.
type OutboxStore interface {
	Claim(context.Context, string, time.Duration) (*OutboxRecord, error)
	CanSend(context.Context, OutboxRecord) (bool, error)
	Complete(context.Context, OutboxRecord) (bool, error)
	Retry(context.Context, OutboxRecord, time.Duration) (bool, error)
}
