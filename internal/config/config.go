// Package config carrega e valida a configuração do serviço a partir de
// variáveis de ambiente. Configuração inválida impede o processo de subir.
package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Role é um papel que uma instância pode exercer (ARCHITECTURE.md §1).
type Role string

const (
	RoleAPI             Role = "api"
	RoleConsumer        Role = "consumer"
	RoleReferenceWorker Role = "reference-worker"
	RoleOutboxPublisher Role = "outbox-publisher"
)

// AllRoles lists supported roles. Load defaults to api; Compose enables all four.
var fifoQueuePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.fifo$`)
var consumerNamePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
var providerPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

var AllRoles = []Role{RoleAPI, RoleConsumer, RoleReferenceWorker, RoleOutboxPublisher}

// Config é a configuração validada do processo.
type Config struct {
	// InstanceID identifica a instância nos logs (INSTANCE_ID ou, na falta, o hostname).
	InstanceID string
	// ProcessID é único por execução do processo, mesmo que duas instâncias
	// recebam o mesmo INSTANCE_ID (ex.: réplicas do Compose com o mesmo .env).
	// É a identidade usada como dona de leases no banco.
	ProcessID string
	Roles     []Role
	LogLevel  slog.Level
	// StartTimeout é o prazo para todos os OnStart (conexões, JWKS, filas).
	StartTimeout    time.Duration
	ShutdownTimeout time.Duration
	// DatabaseURL is required by ValidateRuntime before the application is composed.
	// Never include it in logs; it can contain a password.
	DatabaseURL          string
	DBMaxConns           int32
	DBOperationTimeout   time.Duration
	DBLockTimeout        time.Duration
	DBStatementTimeout   time.Duration
	DBMaxRetries         int
	HTTPAddress          string
	OIDCIssuer           string
	OIDCJWKSURL          string
	OIDCAudience         string
	OIDCInternalClient   string
	SQSEndpoint          string
	AWSRegion            string
	SQSCredentialsFile   string
	ConsumerQueue        string
	ConsumerName         string
	ConsumerProviders    []string
	ConsumerConcurrency  int
	PublisherQueue       string
	PublisherConcurrency int
	ConsumerDLQ          string
	ReferenceTTL         time.Duration
	ReferenceBaseDelay   time.Duration
	ReferenceMaxDelay    time.Duration
	AllowInsecureLocal   bool
}

// Has informa se a instância exerce o papel.
func (c Config) Has(r Role) bool { return slices.Contains(c.Roles, r) }

// Load lê a configuração usando getenv (os.Getenv em produção; um mapa nos testes)
// e devolve todos os problemas encontrados de uma vez.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	cfg := Config{InstanceID: getenv("INSTANCE_ID")}

	if cfg.InstanceID == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			errs = append(errs, errors.New("INSTANCE_ID is required when the hostname is unavailable"))
		}
		cfg.InstanceID = h
	}
	cfg.ProcessID = cfg.InstanceID + "/" + rand.Text()[:10]

	roles, err := parseRoles(getenv("APP_ROLES"))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.Roles = roles

	cfg.LogLevel = slog.LevelInfo
	if v := getenv("LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
		}
	}

	cfg.StartTimeout = duration(getenv, "START_TIMEOUT", 30*time.Second, &errs)
	cfg.ShutdownTimeout = duration(getenv, "SHUTDOWN_TIMEOUT", 20*time.Second, &errs)
	cfg.DatabaseURL = getenv("DATABASE_URL")
	cfg.DBMaxConns = int32(integer(getenv, "DB_MAX_CONNS", 10, 1, 100, &errs))
	cfg.DBMaxRetries = integer(getenv, "DB_MAX_RETRIES", 3, 0, 3, &errs)
	cfg.DBOperationTimeout = duration(getenv, "DB_OPERATION_TIMEOUT", 20*time.Second, &errs)
	cfg.DBLockTimeout = duration(getenv, "DB_LOCK_TIMEOUT", 3*time.Second, &errs)
	cfg.DBStatementTimeout = duration(getenv, "DB_STATEMENT_TIMEOUT", 5*time.Second, &errs)
	cfg.ConsumerQueue = fallback(getenv, "CONSUMER_QUEUE", "wager-transactions.fifo")
	cfg.ConsumerName = fallback(getenv, "CONSUMER_NAME", "wager-consumer")
	cfg.ConsumerProviders = strings.Split(fallback(getenv, "CONSUMER_PROVIDERS", "provider-a,provider-b"), ",")
	cfg.ConsumerConcurrency = integer(getenv, "CONSUMER_CONCURRENCY", 4, 1, 16, &errs)
	cfg.ConsumerDLQ = fallback(getenv, "CONSUMER_DLQ", "wager-transactions-dlq.fifo")
	cfg.PublisherQueue = fallback(getenv, "PUBLISHER_QUEUE", "wallet-events.fifo")
	cfg.PublisherConcurrency = integer(getenv, "PUBLISHER_CONCURRENCY", 4, 1, 16, &errs)
	cfg.ReferenceTTL = duration(getenv, "REFERENCE_TTL", 10*time.Minute, &errs)
	cfg.ReferenceBaseDelay = duration(getenv, "REFERENCE_BASE_DELAY", time.Second, &errs)
	cfg.ReferenceMaxDelay = duration(getenv, "REFERENCE_MAX_DELAY", time.Minute, &errs)
	if cfg.ReferenceTTL > 24*time.Hour || cfg.ReferenceMaxDelay > time.Hour || cfg.ReferenceBaseDelay > cfg.ReferenceMaxDelay || cfg.ReferenceBaseDelay > cfg.ReferenceTTL {
		errs = append(errs, errors.New("reference durations require base <= max <= 1h, base <= TTL <= 24h"))
	}
	cfg.HTTPAddress = fallback(getenv, "HTTP_ADDRESS", ":8080")
	cfg.OIDCIssuer = fallback(getenv, "OIDC_ISSUER", "http://localhost:18080/realms/jungle-gaming")
	cfg.OIDCJWKSURL = fallback(getenv, "OIDC_JWKS_URL", "http://keycloak:8080/realms/jungle-gaming/protocol/openid-connect/certs")
	cfg.OIDCAudience = fallback(getenv, "OIDC_AUDIENCE", "wallet-api")
	cfg.OIDCInternalClient = fallback(getenv, "OIDC_INTERNAL_CLIENT", "wallet-internal")
	cfg.SQSEndpoint = fallback(getenv, "SQS_ENDPOINT", "http://sqs:4566")
	cfg.AWSRegion = fallback(getenv, "AWS_REGION", "us-east-1")
	cfg.SQSCredentialsFile = fallback(getenv, "SQS_CREDENTIALS_FILE", "/wallet-service-credentials/credentials")
	if raw := getenv("ALLOW_INSECURE_LOCAL"); raw != "" {
		cfg.AllowInsecureLocal, err = strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, errors.New("ALLOW_INSECURE_LOCAL must be boolean"))
		}
	}
	if cfg.DBOperationTimeout > time.Minute || cfg.DBStatementTimeout > cfg.DBOperationTimeout || cfg.DBLockTimeout > cfg.DBStatementTimeout {
		errs = append(errs, errors.New("database timeouts require lock <= statement <= operation <= 1m"))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func integer(getenv func(string) string, key string, def, low, high int, errs *[]error) int {
	value := getenv(key)
	if value == "" {
		return def
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < low || n > high {
		*errs = append(*errs, fmt.Errorf("%s must be an integer between %d and %d", key, low, high))
		return def
	}
	return n
}

// duration lê uma duração positiva, com padrão quando ausente.
func duration(getenv func(string) string, key string, def time.Duration, errs *[]error) time.Duration {
	v := getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	switch {
	case err != nil:
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
	case d <= 0:
		*errs = append(*errs, fmt.Errorf("%s must be positive", key))
	default:
		return d
	}
	return def
}

func parseRoles(raw string) ([]Role, error) {
	if strings.TrimSpace(raw) == "" {
		return []Role{RoleAPI}, nil
	}
	var roles []Role
	for part := range strings.SplitSeq(raw, ",") {
		r := Role(strings.TrimSpace(part))
		if !slices.Contains(AllRoles, r) {
			return nil, fmt.Errorf("APP_ROLES: unknown role %q", r)
		}
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	return roles, nil
}

func fallback(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// Runtime requirements are checked by bootstrap before creating resources.
// Load remains usable by tool/tests that only inspect configuration.
func (c Config) ValidateRuntime() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	} else {
		u, err := url.Parse(c.DatabaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() != "wallet_app" {
			errs = append(errs, errors.New("DATABASE_URL must be a PostgreSQL URL using wallet_app"))
		} else {
			query, queryErr := url.ParseQuery(u.RawQuery)
			if queryErr != nil {
				errs = append(errs, errors.New("invalid DATABASE_URL parameters"))
			}
			for _, user := range query["user"] {
				if user != "wallet_app" {
					errs = append(errs, errors.New("DATABASE_URL cannot override wallet_app"))
					break
				}
			}
		}
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddress); err != nil {
		errs = append(errs, errors.New("HTTP_ADDRESS must be host:port"))
	}
	for _, item := range []struct{ name, value string }{{"OIDC_ISSUER", c.OIDCIssuer}, {"OIDC_JWKS_URL", c.OIDCJWKSURL}, {"SQS_ENDPOINT", c.SQSEndpoint}} {
		u, err := url.Parse(item.value)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.AllowInsecureLocal && u.Scheme == "http")) {
			errs = append(errs, fmt.Errorf("%s requires HTTPS (HTTP only with ALLOW_INSECURE_LOCAL)", item.name))
		}
	}
	for _, role := range c.Roles {
		if role != RoleAPI && role != RoleReferenceWorker && role != RoleConsumer && role != RoleOutboxPublisher {
			errs = append(errs, errors.New("unknown runtime role"))
			break
		}
	}
	if c.OIDCAudience != "wallet-api" || c.OIDCInternalClient == "" || c.SQSCredentialsFile == "" {
		errs = append(errs, errors.New("wallet-api audience, internal client and SQS credential file are required"))
	}
	if !fifoQueuePattern.MatchString(c.ConsumerQueue) || len(c.ConsumerQueue) > 80 || !consumerNamePattern.MatchString(c.ConsumerName) || len(c.ConsumerProviders) == 0 {
		errs = append(errs, errors.New("consumer identity, queue and providers required"))
	}
	if !fifoQueuePattern.MatchString(c.ConsumerDLQ) || len(c.ConsumerDLQ) > 80 {
		errs = append(errs, errors.New("invalid consumer DLQ"))
	}
	if !fifoQueuePattern.MatchString(c.PublisherQueue) || len(c.PublisherQueue) > 80 {
		errs = append(errs, errors.New("invalid publisher queue"))
	}
	for _, provider := range c.ConsumerProviders {
		if !providerPattern.MatchString(provider) {
			errs = append(errs, errors.New("invalid consumer provider allowlist"))
			break
		}
	}
	return errors.Join(errs...)
}
