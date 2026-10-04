// Package postgres implements application ports using explicit SQL and pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-backend/internal/application"
)

type Options struct {
	MaxConns         int32
	OperationTimeout time.Duration
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	MaxRetries       int
}

func DefaultOptions() Options {
	return Options{10, 20 * time.Second, 3 * time.Second, 5 * time.Second, 3}
}

func (o Options) validate() error {
	if o.MaxConns < 1 || o.MaxConns > 100 || o.OperationTimeout <= 0 || o.OperationTimeout > time.Minute || o.LockTimeout <= 0 || o.StatementTimeout < o.LockTimeout || o.StatementTimeout > o.OperationTimeout || o.MaxRetries < 0 || o.MaxRetries > 3 {
		return errors.New("invalid database options")
	}
	return nil
}

// AppPoolConfig checks the effective driver identity and never echoes the DSN.
func AppPoolConfig(dsn string, options Options) (*pgxpool.Config, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid database connection configuration")
	}
	if cfg.ConnConfig.User != "wallet_app" {
		return nil, errors.New("effective database user must be wallet_app")
	}
	cfg.MaxConns = options.MaxConns
	cfg.ConnConfig.ConnectTimeout = options.StatementTimeout
	cfg.ConnConfig.RuntimeParams["application_name"] = "jungle-gaming"
	return cfg, nil
}

// OpenPool validates the effective pgx identity before opening any connection.
func OpenPool(ctx context.Context, dsn string, options Options) (*pgxpool.Pool, error) {
	cfg, err := AppPoolConfig(dsn, options)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, application.ErrUnavailable
	}
	pingCtx, cancel := context.WithTimeout(ctx, options.StatementTimeout)
	defer cancel()
	if err = pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, application.ErrUnavailable
	}
	return pool, nil
}

type UnitOfWork struct {
	diagnostics application.Diagnostics
	pool        *pgxpool.Pool
	options     Options
}

func NewUnitOfWork(pool *pgxpool.Pool, options Options) (*UnitOfWork, error) {
	if pool == nil {
		return nil, errors.New("database pool required")
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	return &UnitOfWork{pool: pool, options: options}, nil
}

// Configure before starting any worker or accepting a request.
func (u *UnitOfWork) SetDiagnostics(d application.Diagnostics) { u.diagnostics = d }

func (u *UnitOfWork) Within(parent context.Context, fn func(context.Context, application.Session) error) error {
	ctx, cancel := context.WithTimeout(parent, u.options.OperationTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		err := u.attempt(ctx, fn)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(err) {
			return safeError(err)
		}
		if u.diagnostics != nil {
			u.diagnostics.Count("concurrency_conflict")
		}
		if attempt >= u.options.MaxRetries {
			return application.ErrUnavailable
		}
		if u.diagnostics != nil {
			u.diagnostics.Count("db_retry")
		}
		timer := time.NewTimer(time.Duration(10+rand.IntN(40)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (u *UnitOfWork) attempt(ctx context.Context, fn func(context.Context, application.Session) error) error {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return application.ErrUnavailable
	}
	defer func() {
		// A canceled request must still release its connection/locks promptly.
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	_, err = tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
		fmt.Sprintf("%dms", max(1, u.options.LockTimeout.Milliseconds())), fmt.Sprintf("%dms", max(1, u.options.StatementTimeout.Milliseconds())))
	if err != nil {
		return err
	}
	if err = fn(ctx, &repositories{tx}); err != nil {
		return err
	}
	// Network failures here may mean the commit succeeded. Never retry them
	// internally: the caller retries the original idempotency key instead.
	err = tx.Commit(ctx)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return err
		}
		return application.ErrUnavailable
	}
	return nil
}

func retryable(err error) bool {
	if errors.Is(err, application.ErrConcurrentDuplicate) {
		return true
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40P01" || pgErr.Code == "40001" || pgErr.Code == "55P03"
}

// DatabaseError omits SQL, input, Detail and connection information. A schema
// invariant failure remains an internal error, never a business rejection.
type DatabaseError struct{ SQLState, Constraint string }

func (e *DatabaseError) Error() string {
	return "database rule failed: " + e.SQLState + " " + e.Constraint
}

func safeError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23505" && pgErr.ConstraintName == "wallets_player_currency_unique" {
			return application.ErrWalletConflict
		}
		if pgErr.Code == "57014" || strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "53") || pgErr.Code == "57P01" || pgErr.Code == "57P02" || pgErr.Code == "57P03" {
			return application.ErrUnavailable
		}
		return &DatabaseError{pgErr.Code, pgErr.ConstraintName}
	}
	var connectErr *pgconn.ConnectError
	var networkErr net.Error
	if errors.As(err, &connectErr) || errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || pgconn.SafeToRetry(err) {
		return application.ErrUnavailable
	}
	return err
}

// Database errors reaching this type survived transient/conflict classification.
func (e *DatabaseError) Permanent() bool { return true }
