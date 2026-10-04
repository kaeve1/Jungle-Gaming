// Package domain contém as entidades e regras financeiras: carteira,
// transação de aposta, ledger, eventos e decisão de processamento. Depende
// apenas da biblioteca padrão e de domain/money (imposto por internal/archtest).
package domain

import (
	"encoding/hex"
	"fmt"

	"jungle-gaming-backend/internal/domain/money"
)

// ID é um UUID. O valor zero é inválido (não inicializado).
type ID [16]byte

// ParseID aceita a forma canônica 8-4-4-4-12, em maiúsculas ou minúsculas.
// String sempre devolve minúsculas, então a forma persistida e a usada no
// hash de idempotência é única.
func ParseID(s string) (ID, error) {
	var id ID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return ID{}, fmt.Errorf("%w: invalid UUID %s", ErrValidation, money.Clip(s))
	}
	hexOnly := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if _, err := hex.Decode(id[:], []byte(hexOnly)); err != nil {
		return ID{}, fmt.Errorf("%w: invalid UUID %s", ErrValidation, money.Clip(s))
	}
	if id.IsZero() {
		return ID{}, fmt.Errorf("%w: nil UUID", ErrValidation)
	}
	return id, nil
}

// IsZero informa se o ID não foi inicializado.
func (id ID) IsZero() bool { return id == ID{} }

func (id ID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], id[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], id[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], id[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], id[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], id[10:16])
	return string(b[:])
}

// MarshalText permite usar ID diretamente em JSON. IDs zero não são serializados.
func (id ID) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("%w: nil UUID", ErrUninitialized)
	}
	return []byte(id.String()), nil
}

// UnmarshalText valida o UUID.
func (id *ID) UnmarshalText(b []byte) error {
	parsed, err := ParseID(string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// IDGenerator gera identificadores novos (UUIDv7 na infraestrutura; sequência nos testes).
type IDGenerator interface {
	NewID() ID
}
