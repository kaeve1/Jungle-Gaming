// Package bootstrap compõe a aplicação com Uber Fx. É o único pacote que
// conhece todos os módulos; o domínio não importa Fx.
package bootstrap

import (
	"context"
	"io"
	httpapi "jungle-gaming-backend/internal/adapters/http"
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"jungle-gaming-backend/internal/config"
)

// Options devolve a composição completa do serviço para uma configuração já
// validada. O prazo de encerramento precisa ser conhecido antes de o grafo
// existir, por isso a configuração é carregada fora do Fx (ver New).
func Options(cfg config.Config, logOut io.Writer) fx.Option {
	return fx.Options(
		fx.Supply(cfg),
		fx.Provide(func(cfg config.Config) *slog.Logger { return newLogger(cfg, logOut) }),
		fx.WithLogger(func(l *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: l} }),
		fx.StartTimeout(cfg.StartTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout),
		RuntimeModule,
	)
}

// New valida a configuração e cria a aplicação. Configuração inválida
// impede o início, antes de qualquer conexão.
func New(getenv func(string) string, logOut io.Writer) (*fx.App, error) {
	instance, err := NewInstance(getenv, logOut)
	if err != nil {
		return nil, err
	}
	return instance.app, nil
}

// Instance exposes lifecycle and listener address without exporting Fx outside
// the composition package. It is also the real instance used by integration.
type Instance struct {
	app    *fx.App
	server *httpapi.Server
}

func (i *Instance) Start(ctx context.Context) error { return i.app.Start(ctx) }
func (i *Instance) Stop(ctx context.Context) error  { return i.app.Stop(ctx) }
func (i *Instance) Address() string                 { return i.server.Address() }

func NewInstance(getenv func(string) string, logOut io.Writer) (*Instance, error) {
	cfg, err := config.Load(getenv)
	if err != nil {
		return nil, err
	}
	if err := cfg.ValidateRuntime(); err != nil {
		return nil, err
	}
	var server *httpapi.Server
	app := fx.New(Options(cfg, logOut), fx.Populate(&server))
	if err := app.Err(); err != nil {
		return nil, err
	}
	return &Instance{app, server}, nil
}

func newLogger(cfg config.Config, out io.Writer) *slog.Logger {
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: cfg.LogLevel})
	return slog.New(h).With("instanceId", cfg.InstanceID, "processId", cfg.ProcessID)
}
