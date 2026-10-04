package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"jungle-gaming-backend/internal/adapters/auth"
	"jungle-gaming-backend/internal/adapters/health"
	httpapi "jungle-gaming-backend/internal/adapters/http"
	"jungle-gaming-backend/internal/adapters/observability"
	"jungle-gaming-backend/internal/adapters/outboxpublisher"
	"jungle-gaming-backend/internal/adapters/postgres"
	"jungle-gaming-backend/internal/adapters/referenceworker"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/adapters/sqsclient"
	"jungle-gaming-backend/internal/adapters/sqsconsumer"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/config"
	"jungle-gaming-backend/internal/domain"
)

var RuntimeModule = fx.Module("runtime", fx.Provide(newVerifier, newResources, newDiagnostics, newUnitOfWork, newReader, newService, newHandler, newServer), fx.Invoke(startReferenceWorker, startConsumer, startPublisher, startServer))

func dbOptions(c config.Config) postgres.Options {
	return postgres.Options{MaxConns: c.DBMaxConns, OperationTimeout: c.DBOperationTimeout, LockTimeout: c.DBLockTimeout, StatementTimeout: c.DBStatementTimeout, MaxRetries: c.DBMaxRetries}
}

func newDiagnostics(lc fx.Lifecycle, pool *pgxpool.Pool, log *slog.Logger, c config.Config) (*observability.Diagnostics, error) {
	client, closeClient, err := sqsclient.New(context.Background(), c.SQSEndpoint, c.AWSRegion, c.SQSCredentialsFile, 25*time.Second)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error { closeClient(); return nil }})
	return observability.New(pool, client, log, c.ConsumerDLQ), nil
}
func newVerifier(c config.Config) *auth.Verifier {
	return auth.NewVerifier(c.OIDCIssuer, c.OIDCJWKSURL, c.OIDCAudience, c.OIDCInternalClient)
}
func newResources(lc fx.Lifecycle, c config.Config, v *auth.Verifier) (*pgxpool.Pool, *health.Checker, error) {
	cfg, err := postgres.AppPoolConfig(c.DatabaseURL, dbOptions(c))
	if err != nil {
		return nil, nil, errors.New("invalid database configuration")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, nil, errors.New("database pool unavailable")
	}
	checker, err := health.NewChecker(context.Background(), pool, c.SQSEndpoint, c.AWSRegion, c.SQSCredentialsFile, []string{c.ConsumerQueue, c.PublisherQueue})
	if err != nil {
		pool.Close()
		v.Close()
		return nil, nil, err
	}
	cleanup := func() { checker.Close(); v.Close(); pool.Close() }
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			cleanup()
			return errors.New("PostgreSQL unavailable at startup")
		}
		if err := v.Probe(ctx); err != nil {
			cleanup()
			return err
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for _, result := range checker.Check(probe) {
			if result != "ok" {
				cleanup()
				return errors.New("PostgreSQL/SQS not ready at startup")
			}
		}
		return nil
	}, OnStop: func(context.Context) error { cleanup(); return nil }})
	return pool, checker, nil
}
func newUnitOfWork(pool *pgxpool.Pool, c config.Config, d *observability.Diagnostics) (*postgres.UnitOfWork, error) {
	u, err := postgres.NewUnitOfWork(pool, dbOptions(c))
	if err == nil {
		u.SetDiagnostics(d)
	}
	return u, err
}
func newReader(pool *pgxpool.Pool, c config.Config) *postgres.Reader {
	return postgres.NewReader(pool, c.DBOperationTimeout)
}
func newService(uow *postgres.UnitOfWork, c config.Config, d *observability.Diagnostics) (*application.Service, error) {
	return application.NewService(uow, appRuntime.IDs{}, domain.ReferencePolicy{TTL: c.ReferenceTTL, BaseDelay: c.ReferenceBaseDelay, MaxDelay: c.ReferenceMaxDelay}, d)
}
func newHandler(service *application.Service, reader *postgres.Reader, verifier *auth.Verifier, ready *health.Checker, log *slog.Logger, d *observability.Diagnostics) http.Handler {
	return httpapi.NewHandler(service, reader, verifier, ready, appRuntime.IDs{}, log, d, d)
}
func newServer(c config.Config, handler http.Handler) *httpapi.Server {
	return httpapi.NewServer(c.HTTPAddress, handler, c.DBOperationTimeout)
}
func startServer(lc fx.Lifecycle, server *httpapi.Server, shutdown fx.Shutdowner, log *slog.Logger, c config.Config) {
	if !c.Has(config.RoleAPI) {
		return
	}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		err := server.Start(ctx, func() { _ = shutdown.Shutdown(fx.ExitCode(1)) })
		if err == nil {
			log.Info("HTTP server started", "address", server.Address())
		}
		return err
	}, OnStop: server.Stop})
}

func startReferenceWorker(lc fx.Lifecycle, service *application.Service, log *slog.Logger, c config.Config) {
	if !c.Has(config.RoleReferenceWorker) {
		return
	}
	worker := referenceworker.New(service, log)
	lc.Append(fx.Hook{OnStart: worker.Start, OnStop: worker.Stop})
}

func startConsumer(lc fx.Lifecycle, service *application.Service, pool *pgxpool.Pool, log *slog.Logger, c config.Config, d *observability.Diagnostics) error {
	if !c.Has(config.RoleConsumer) {
		return nil
	}
	client, closeClient, err := sqsclient.New(context.Background(), c.SQSEndpoint, c.AWSRegion, c.SQSCredentialsFile, 25*time.Second)
	if err != nil {
		return err
	}
	worker, err := sqsconsumer.New(client, service, pool, log, sqsconsumer.Options{Queue: c.ConsumerQueue, Consumer: c.ConsumerName, Providers: c.ConsumerProviders, Concurrency: c.ConsumerConcurrency})
	if err != nil {
		closeClient()
		return err
	}
	worker.SetDiagnostics(d)
	lc.Append(fx.Hook{OnStop: func(context.Context) error { closeClient(); return nil }})
	lc.Append(fx.Hook{OnStart: worker.Start, OnStop: worker.Stop})
	return nil
}

func startPublisher(lc fx.Lifecycle, pool *pgxpool.Pool, log *slog.Logger, c config.Config, d *observability.Diagnostics) error {
	if !c.Has(config.RoleOutboxPublisher) {
		return nil
	}
	client, closeClient, err := sqsclient.New(context.Background(), c.SQSEndpoint, c.AWSRegion, c.SQSCredentialsFile, 25*time.Second)
	if err != nil {
		return err
	}
	worker, err := outboxpublisher.New(client, postgres.NewOutbox(pool), log, c.PublisherQueue, c.ProcessID, c.PublisherConcurrency)
	if err != nil {
		closeClient()
		return err
	}
	worker.SetDiagnostics(d)
	lc.Append(fx.Hook{OnStop: func(context.Context) error { closeClient(); return nil }})
	lc.Append(fx.Hook{OnStart: worker.Start, OnStop: worker.Stop})
	return nil
}
