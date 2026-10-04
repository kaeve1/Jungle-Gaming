// Package money implementa o value object Money: valor exato em unidades
// mínimas (int64) com moeda ISO 4217. Nenhum caminho usa ponto flutuante.
// Decisões e limites em ARCHITECTURE.md §3.
package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Erros classificáveis com errors.Is.
var (
	ErrInvalidAmount       = errors.New("money: invalid amount format")
	ErrInvalidScale        = errors.New("money: amount must have exactly 2 decimal places")
	ErrNegativeAmount      = errors.New("money: negative amount not allowed")
	ErrOverflow            = errors.New("money: amount out of range")
	ErrUnsupportedCurrency = errors.New("money: unsupported currency")
	ErrCurrencyMismatch    = errors.New("money: currency mismatch")
	ErrUninitialized       = errors.New("money: uninitialized value")
)

const (
	scale         = 2
	unitsPerMajor = 100 // 10^scale
	// maxAmountLen limita o trabalho de parsing antes de qualquer conversão;
	// o maior valor válido tem 20 caracteres ("92233720368547758.07").
	maxAmountLen = 32
)

// Currency é um código ISO 4217 suportado. O valor zero é inválido.
type Currency struct{ code string }

// Moedas suportadas (todas com 2 casas decimais).
var (
	BRL = Currency{"BRL"}
	USD = Currency{"USD"}
	EUR = Currency{"EUR"}
)

var supported = map[string]Currency{"BRL": BRL, "USD": USD, "EUR": EUR}

// ParseCurrency aceita apenas códigos suportados, em maiúsculas, sem normalização.
func ParseCurrency(code string) (Currency, error) {
	c, ok := supported[code]
	if !ok {
		return Currency{}, fmt.Errorf("%w: %s", ErrUnsupportedCurrency, Clip(code))
	}
	return c, nil
}

// String devolve o código ISO 4217.
func (c Currency) String() string { return c.code }

// IsZero informa se a moeda não foi inicializada.
func (c Currency) IsZero() bool { return c.code == "" }

// Money é imutável: operações devolvem um novo valor. O valor zero de Go
// (Money{}) é inválido e é recusado por todas as operações.
type Money struct {
	units    int64
	currency Currency
}

// Parse lê um valor de entrada externa: formato estrito "123.45", não negativo.
// Recusa vazio, sinal, espaços, notação científica, NaN, Infinity, zeros à
// esquerda e escala diferente de 2. Não arredonda nem normaliza.
func Parse(amount, currency string) (Money, error) {
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	if strings.HasPrefix(amount, "-") {
		return Money{}, fmt.Errorf("%w: %s", ErrNegativeAmount, Clip(amount))
	}
	mag, err := parseMagnitude(amount, math.MaxInt64)
	if err != nil {
		return Money{}, err
	}
	return Money{units: int64(mag), currency: c}, nil
}

// ParseSigned lê valores internos que podem ser negativos (diferenças,
// reconciliação). "-0.00" é recusado: o zero tem uma única forma.
func ParseSigned(amount, currency string) (Money, error) {
	rest, neg := strings.CutPrefix(amount, "-")
	if !neg {
		return Parse(amount, currency)
	}
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	mag, err := parseMagnitude(rest, uint64(math.MaxInt64)+1)
	if err != nil {
		return Money{}, err
	}
	if mag == 0 {
		return Money{}, fmt.Errorf("%w: negative zero %s", ErrInvalidAmount, Clip(amount))
	}
	return Money{units: int64(-mag), currency: c}, nil // -2^63 cabe em int64
}

// parseMagnitude interpreta "intPart.ff" como unidades mínimas, até limit.
func parseMagnitude(s string, limit uint64) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("%w: empty", ErrInvalidAmount)
	}
	if len(s) > maxAmountLen {
		return 0, fmt.Errorf("%w: %s", ErrOverflow, Clip(s))
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if !isDigits(intPart) || (hasDot && !isDigits(frac) && frac != "") {
		return 0, fmt.Errorf("%w: %s", ErrInvalidAmount, Clip(s))
	}
	if !hasDot || len(frac) != scale {
		return 0, fmt.Errorf("%w: %s", ErrInvalidScale, Clip(s))
	}
	if len(intPart) > 1 && intPart[0] == '0' {
		return 0, fmt.Errorf("%w: leading zero in %s", ErrInvalidAmount, Clip(s))
	}
	f := uint64(frac[0]-'0')*10 + uint64(frac[1]-'0')
	var v uint64
	for i := 0; i < len(intPart); i++ {
		d := uint64(intPart[i] - '0')
		if v > (limit-d)/10 {
			return 0, fmt.Errorf("%w: %s", ErrOverflow, Clip(s))
		}
		v = v*10 + d
	}
	if v > (limit-f)/unitsPerMajor {
		return 0, fmt.Errorf("%w: %s", ErrOverflow, Clip(s))
	}
	return v*unitsPerMajor + f, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Zero devolve o zero da moeda.
func Zero(c Currency) (Money, error) { return FromMinorUnits(0, c) }

// FromMinorUnits reconstrói um valor a partir de unidades mínimas (ex.: BIGINT do banco).
func FromMinorUnits(units int64, c Currency) (Money, error) {
	if c.IsZero() {
		return Money{}, ErrUninitialized
	}
	return Money{units: units, currency: c}, nil
}

// Valid informa se o valor foi inicializado por um construtor.
func (m Money) Valid() bool { return !m.currency.IsZero() }

// MinorUnits devolve o valor em unidades mínimas (centavos).
func (m Money) MinorUnits() int64 { return m.units }

// Currency devolve a moeda.
func (m Money) Currency() Currency { return m.currency }

// IsZero, IsPositive e IsNegative são falsos para valores não inicializados.
func (m Money) IsZero() bool     { return m.Valid() && m.units == 0 }
func (m Money) IsPositive() bool { return m.Valid() && m.units > 0 }
func (m Money) IsNegative() bool { return m.Valid() && m.units < 0 }

func (m Money) compatible(o Money) error {
	if !m.Valid() || !o.Valid() {
		return ErrUninitialized
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}

// Add soma valores da mesma moeda, com verificação de overflow.
func (m Money) Add(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.units > 0 && m.units > math.MaxInt64-o.units) || (o.units < 0 && m.units < math.MinInt64-o.units) {
		return Money{}, ErrOverflow
	}
	return Money{units: m.units + o.units, currency: m.currency}, nil
}

// Sub subtrai valores da mesma moeda, com verificação de overflow.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.units < 0 && m.units > math.MaxInt64+o.units) || (o.units > 0 && m.units < math.MinInt64+o.units) {
		return Money{}, ErrOverflow
	}
	return Money{units: m.units - o.units, currency: m.currency}, nil
}

// Neg devolve o oposto. O menor int64 não tem oposto representável.
func (m Money) Neg() (Money, error) {
	if !m.Valid() {
		return Money{}, ErrUninitialized
	}
	if m.units == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{units: -m.units, currency: m.currency}, nil
}

// Cmp devolve -1, 0 ou 1. Exige moedas iguais.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.compatible(o); err != nil {
		return 0, err
	}
	switch {
	case m.units < o.units:
		return -1, nil
	case m.units > o.units:
		return 1, nil
	}
	return 0, nil
}

// Amount devolve o valor decimal canônico ("25.00", "-5.00").
func (m Money) Amount() string {
	mag := uint64(m.units)
	sign := ""
	if m.units < 0 {
		sign = "-"
		mag = uint64(-(m.units + 1)) + 1 // evita overflow em -2^63
	}
	return sign + strconv.FormatUint(mag/unitsPerMajor, 10) + "." + fmt.Sprintf("%02d", mag%unitsPerMajor)
}

// String devolve "25.00 BRL" (para logs e mensagens; não é o formato do contrato).
func (m Money) String() string {
	if !m.Valid() {
		return "<invalid money>"
	}
	return m.Amount() + " " + m.currency.code
}

// MarshalJSON produz {"amount":"25.00","currency":"BRL"}.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.Valid() {
		return nil, ErrUninitialized
	}
	return []byte(`{"amount":"` + m.Amount() + `","currency":"` + m.currency.code + `"}`), nil
}

// UnmarshalJSON aceita apenas a forma do contrato externo: objeto com
// exatamente as chaves "amount" e "currency" (com essa grafia, sem repetição),
// ambas strings. Valores negativos são recusados.
//
// O decodificador padrão do Go compara chaves sem diferenciar maiúsculas e,
// com chave repetida, fica com a última. Aqui as duas coisas são recusadas,
// para que nenhum outro leitor do mesmo JSON enxergue um valor diferente do
// que foi aplicado (VULN-005).
func (m *Money) UnmarshalJSON(b []byte) error {
	raw, err := strictObject(b)
	if err != nil {
		return err
	}
	if len(raw) != 2 || raw["amount"] == nil || raw["currency"] == nil {
		return fmt.Errorf("%w: money must have exactly \"amount\" and \"currency\"", ErrInvalidAmount)
	}
	var amount, currency string
	if err := json.Unmarshal(raw["amount"], &amount); err != nil {
		return fmt.Errorf("%w: amount must be a string", ErrInvalidAmount)
	}
	if err := json.Unmarshal(raw["currency"], &currency); err != nil {
		return fmt.Errorf("%w: currency must be a string", ErrUnsupportedCurrency)
	}
	parsed, err := Parse(amount, currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// strictObject lê um objeto JSON recusando chaves repetidas.
func strictObject(b []byte) (map[string]json.RawMessage, error) {
	notObject := fmt.Errorf("%w: money must be an object", ErrInvalidAmount)
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, notObject
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return nil, notObject
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%w: duplicate key %s", ErrInvalidAmount, Clip(key))
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, notObject
		}
		out[key] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, notObject
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, notObject
	}
	return out, nil
}

// maxEcho limita quanto de uma entrada externa aparece em mensagens de erro.
const maxEcho = 32

// Clip devolve a entrada entre aspas e escapada, cortada em maxEcho bytes.
// Mensagens de erro chegam a logs e respostas; ecoar entrada sem limite
// permitiria inflar logs com corpos enormes (VULN-006).
func Clip(s string) string {
	if len(s) <= maxEcho {
		return strconv.Quote(s)
	}
	return strconv.Quote(s[:maxEcho]) + fmt.Sprintf("...(%d bytes)", len(s))
}
