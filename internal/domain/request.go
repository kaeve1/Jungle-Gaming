package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"jungle-gaming-backend/internal/domain/money"
)

// FingerprintVersion identifica o algoritmo do hash de idempotência
// (ARCHITECTURE.md §6). Gravado junto com o hash.
const FingerprintVersion = 1

const (
	maxExternalIDLen     = 128
	maxIdempotencyKeyLen = 255
)

// RawExternalRequest é o pedido como chega de HTTP ou SQS, antes da validação.
// Os dois adaptadores preenchem esta mesma estrutura e chamam NewExternalRequest,
// garantindo regras e hash idênticos entre os transportes.
type RawExternalRequest struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	Money                          money.Money
	ReferenceExternalTransactionID string // vazio = ausente
}

// ExternalRequest é um pedido externo já validado. Só pode ser obtido por
// NewExternalRequest; o valor zero é inválido.
type ExternalRequest struct {
	providerID     string
	externalID     string
	idempotencyKey string
	playerID       ID
	walletID       ID
	roundID        string
	gameID         string
	kind           Kind
	amount         money.Money
	referenceExtID string
}

// NewExternalRequest valida o pedido e devolve o primeiro problema encontrado
// como *ValidationError (errors.Is(err, ErrValidation)).
func NewExternalRequest(r RawExternalRequest) (ExternalRequest, error) {
	for _, f := range []struct{ name, value string }{
		{"providerId", r.ProviderID},
		{"externalTransactionId", r.ExternalTransactionID},
		{"roundId", r.RoundID},
		{"gameId", r.GameID},
	} {
		if !validExternalID(f.value) {
			return ExternalRequest{}, invalid(CodeValidationError, f.name, "must be 1-128 characters of [A-Za-z0-9._:-]")
		}
	}
	if r.IdempotencyKey == "" {
		return ExternalRequest{}, invalid(CodeMissingIdempotencyKey, "idempotencyKey", "is required")
	}
	if !validIdempotencyKey(r.IdempotencyKey) {
		return ExternalRequest{}, invalid(CodeValidationError, "idempotencyKey", "must be 1-255 visible ASCII characters")
	}
	playerID, err := ParseID(r.PlayerID)
	if err != nil {
		return ExternalRequest{}, invalid(CodeValidationError, "playerId", "must be a UUID")
	}
	walletID, err := ParseID(r.WalletID)
	if err != nil {
		return ExternalRequest{}, invalid(CodeValidationError, "walletId", "must be a UUID")
	}
	kind := Kind(r.Kind)
	if !kind.IsExternal() {
		return ExternalRequest{}, invalid(CodeInvalidKind, "kind", "must be BET, WIN, LOSS, REFUND or ROLLBACK")
	}
	if !r.Money.Valid() || r.Money.IsNegative() {
		return ExternalRequest{}, invalid(CodeInvalidAmount, "money", "must be a valid non-negative amount")
	}
	if kind == KindLoss && !r.Money.IsZero() {
		return ExternalRequest{}, invalid(CodeInvalidAmount, "money.amount", "LOSS requires \"0.00\"")
	}
	if kind != KindLoss && !r.Money.IsPositive() {
		return ExternalRequest{}, invalid(CodeInvalidAmount, "money.amount", string(kind)+" requires a positive amount")
	}

	ref := r.ReferenceExternalTransactionID
	switch kind.referencePolicy() {
	case referenceRequired:
		if ref == "" {
			return ExternalRequest{}, invalid(CodeValidationError, "referenceExternalTransactionId", "is required for "+string(kind))
		}
	case referenceForbidden:
		if ref != "" {
			return ExternalRequest{}, invalid(CodeValidationError, "referenceExternalTransactionId", "is not allowed for "+string(kind))
		}
	}
	if ref != "" {
		if !validExternalID(ref) {
			return ExternalRequest{}, invalid(CodeValidationError, "referenceExternalTransactionId", "must be 1-128 characters of [A-Za-z0-9._:-]")
		}
		if ref == r.ExternalTransactionID {
			return ExternalRequest{}, invalid(CodeValidationError, "referenceExternalTransactionId", "cannot reference itself")
		}
	}

	return ExternalRequest{
		providerID: r.ProviderID, externalID: r.ExternalTransactionID, idempotencyKey: r.IdempotencyKey,
		playerID: playerID, walletID: walletID, roundID: r.RoundID, gameID: r.GameID,
		kind: kind, amount: r.Money, referenceExtID: ref,
	}, nil
}

func (r ExternalRequest) ProviderID() string                     { return r.providerID }
func (r ExternalRequest) ExternalTransactionID() string          { return r.externalID }
func (r ExternalRequest) IdempotencyKey() string                 { return r.idempotencyKey }
func (r ExternalRequest) PlayerID() ID                           { return r.playerID }
func (r ExternalRequest) WalletID() ID                           { return r.walletID }
func (r ExternalRequest) RoundID() string                        { return r.roundID }
func (r ExternalRequest) GameID() string                         { return r.gameID }
func (r ExternalRequest) Kind() Kind                             { return r.kind }
func (r ExternalRequest) Money() money.Money                     { return r.amount }
func (r ExternalRequest) ReferenceExternalTransactionID() string { return r.referenceExtID }

// CanonicalJSON é a representação usada no hash: só campos de negócio, chaves
// em ordem lexicográfica (encoding/json ordena chaves de mapas), sem espaços e
// sem escape de HTML. Exclui a chave de idempotência e metadados de transporte.
func (r ExternalRequest) CanonicalJSON() []byte {
	fields := map[string]any{
		"externalTransactionId": r.externalID,
		"gameId":                r.gameID,
		"kind":                  string(r.kind),
		"money":                 map[string]string{"amount": r.amount.Amount(), "currency": r.amount.Currency().String()},
		"playerId":              r.playerID.String(),
		"providerId":            r.providerID,
		"roundId":               r.roundID,
		"walletId":              r.walletID.String(),
	}
	if r.referenceExtID != "" {
		fields["referenceExternalTransactionId"] = r.referenceExtID
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(fields) // só strings: não falha
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// Fingerprint é o SHA-256 (hex minúsculo) do JSON canônico, versão FingerprintVersion.
func (r ExternalRequest) Fingerprint() string {
	sum := sha256.Sum256(r.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

func validExternalID(s string) bool {
	if len(s) == 0 || len(s) > maxExternalIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == ':' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func validIdempotencyKey(s string) bool {
	if len(s) == 0 || len(s) > maxIdempotencyKeyLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// DuplicateKind classifica um pedido que colide com uma transação já registrada.
type DuplicateKind int

const (
	// Replay: mesma chave e mesmo conteúdo → devolver o resultado persistido.
	Replay DuplicateKind = iota + 1
	// KeyReused: mesma chave com conteúdo diferente → conflito.
	KeyReused
	// ExternalIDConflict: mesma operação (provedor + ID externo) com outra chave → conflito.
	ExternalIDConflict
)

// ClassifyDuplicate compara o pedido com a transação existente encontrada por
// (provedor, chave) ou (provedor, ID externo). Erro se a transação não colide.
func ClassifyDuplicate(existing *WagerTransaction, req ExternalRequest) (DuplicateKind, error) {
	if existing == nil || existing.id.IsZero() || req.kind == "" {
		return 0, ErrUninitialized
	}
	ext := existing.External()
	if ext == nil || ext.ProviderID != req.providerID {
		return 0, invariant("transaction does not belong to provider %s", money.Clip(req.providerID))
	}
	if ext.RequestHashVersion != FingerprintVersion {
		// Uma futura versão do algoritmo exigirá recalcular o hash antigo antes
		// de comparar; comparar hashes de versões diferentes daria falso conflito.
		return 0, invariant("request hash version %d is not supported", ext.RequestHashVersion)
	}
	switch {
	case ext.IdempotencyKey == req.idempotencyKey && ext.RequestHash == req.Fingerprint():
		return Replay, nil
	case ext.IdempotencyKey == req.idempotencyKey:
		return KeyReused, nil
	case ext.ExternalTransactionID == req.externalID:
		return ExternalIDConflict, nil
	}
	return 0, invariant("transaction does not collide with request")
}
