// Package health checks real PostgreSQL and SQS without exposing diagnostics.
package health

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-backend/internal/adapters/sqsclient"
)

type Checker struct {
	pool        *pgxpool.Pool
	client      *sqs.Client
	closeClient func()
	queues      []string
}

func NewChecker(ctx context.Context, pool *pgxpool.Pool, endpoint, region, file string, queues []string) (*Checker, error) {
	client, closeClient, err := sqsclient.New(ctx, endpoint, region, file, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return &Checker{pool: pool, client: client, closeClient: closeClient, queues: append([]string(nil), queues...)}, nil
}
func (c *Checker) Close() { c.closeClient() }

func (c *Checker) Check(ctx context.Context) map[string]string {
	var wg sync.WaitGroup
	var pgErr, sqsErr error
	wg.Go(func() { pgErr = c.pool.Ping(ctx) })
	wg.Go(func() {
		for _, name := range c.queues {
			out, err := c.client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
			if err != nil || out.QueueUrl == nil {
				sqsErr = errors.New("queue unavailable")
				return
			}
		}
	})
	wg.Wait()
	result := map[string]string{"postgres": "ok", "sqs": "ok"}
	if pgErr != nil {
		result["postgres"] = "unavailable"
	}
	if sqsErr != nil {
		result["sqs"] = "unavailable"
	}
	return result
}
