package money

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

// must interrompe o teste se a construção falhar (o panic vira falha do teste).
func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

func TestParseValid(t *testing.T) {
	cases := map[string]int64{
		"0.00":                 0,
		"0.01":                 1,
		"25.00":                2500,
		"1000.50":              100050,
		"92233720368547758.07": math.MaxInt64,
	}
	for in, want := range cases {
		m, err := Parse(in, "BRL")
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if m.MinorUnits() != want || m.Currency() != BRL {
			t.Errorf("Parse(%q) = %d %s, want %d BRL", in, m.MinorUnits(), m.Currency(), want)
		}
		if m.Amount() != in {
			t.Errorf("round trip %q -> %q", in, m.Amount())
		}
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{"", ErrInvalidAmount},
		{"NaN", ErrInvalidAmount},
		{"Infinity", ErrInvalidAmount},
		{"-Infinity", ErrNegativeAmount},
		{"1e3", ErrInvalidAmount},
		{"1.5e2", ErrInvalidAmount},
		{"2.5E+01", ErrInvalidAmount},
		{"+25.00", ErrInvalidAmount},
		{"-25.00", ErrNegativeAmount},
		{" 25.00", ErrInvalidAmount},
		{"25.00 ", ErrInvalidAmount},
		{"25,00", ErrInvalidAmount},
		{"025.00", ErrInvalidAmount},
		{"00.00", ErrInvalidAmount},
		{".50", ErrInvalidAmount},
		{"0x10.00", ErrInvalidAmount},
		{"1_000.00", ErrInvalidAmount},
		{"25.0x", ErrInvalidAmount},
		{"25", ErrInvalidScale},
		{"25.", ErrInvalidScale},
		{"25.0", ErrInvalidScale},
		{"25.000", ErrInvalidScale},
		{"0.001", ErrInvalidScale},
		{"92233720368547758.08", ErrOverflow},
		{"99999999999999999999.99", ErrOverflow},
		{"1000000000000000000000000000000000.00", ErrOverflow},
		{"٣.٠٠", ErrInvalidAmount}, // dígitos não ASCII
	}
	for _, c := range cases {
		_, err := Parse(c.in, "BRL")
		if !errors.Is(err, c.want) {
			t.Errorf("Parse(%q) error = %v, want %v", c.in, err, c.want)
		}
	}
}

func TestParseCurrency(t *testing.T) {
	for _, code := range []string{"BRL", "USD", "EUR"} {
		if _, err := ParseCurrency(code); err != nil {
			t.Errorf("ParseCurrency(%q): %v", code, err)
		}
	}
	for _, code := range []string{"", "brl", "Brl", "JPY", "XYZ", "BRL ", "BR"} {
		if _, err := ParseCurrency(code); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Errorf("ParseCurrency(%q) error = %v", code, err)
		}
		if _, err := Parse("1.00", code); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Errorf("Parse with currency %q error = %v", code, err)
		}
	}
}

func TestParseSigned(t *testing.T) {
	m := must(ParseSigned("-5.00", "BRL"))
	if m.MinorUnits() != -500 || m.Amount() != "-5.00" || !m.IsNegative() {
		t.Errorf("got %v", m)
	}
	m = must(ParseSigned("-92233720368547758.08", "BRL"))
	if m.MinorUnits() != math.MinInt64 || m.Amount() != "-92233720368547758.08" {
		t.Errorf("min int64: got %d %q", m.MinorUnits(), m.Amount())
	}
	for in, want := range map[string]error{
		"-0.00":                 ErrInvalidAmount,
		"--5.00":                ErrInvalidAmount,
		"-":                     ErrInvalidAmount,
		"-5":                    ErrInvalidScale,
		"-92233720368547758.09": ErrOverflow,
	} {
		if _, err := ParseSigned(in, "BRL"); !errors.Is(err, want) {
			t.Errorf("ParseSigned(%q) error = %v, want %v", in, err, want)
		}
	}
}

func TestArithmetic(t *testing.T) {
	a := must(Parse("100.00", "BRL"))
	b := must(Parse("80.00", "BRL"))

	sum := must(a.Add(b))
	if sum.Amount() != "180.00" {
		t.Errorf("Add = %s", sum.Amount())
	}
	diff := must(b.Sub(a))
	if diff.Amount() != "-20.00" {
		t.Errorf("Sub = %s", diff.Amount())
	}
	neg := must(a.Neg())
	if neg.Amount() != "-100.00" {
		t.Errorf("Neg = %s", neg.Amount())
	}
	if c, _ := a.Cmp(b); c != 1 {
		t.Errorf("Cmp(100, 80) = %d", c)
	}
	if c, _ := b.Cmp(a); c != -1 {
		t.Errorf("Cmp(80, 100) = %d", c)
	}
	if c, _ := a.Cmp(a); c != 0 {
		t.Errorf("Cmp(a, a) = %d", c)
	}
	if a.Amount() != "100.00" || b.Amount() != "80.00" {
		t.Error("operations must not mutate operands")
	}
}

func TestOverflow(t *testing.T) {
	maxV := must(FromMinorUnits(math.MaxInt64, BRL))
	minV := must(FromMinorUnits(math.MinInt64, BRL))
	one := must(FromMinorUnits(1, BRL))

	if _, err := maxV.Add(one); !errors.Is(err, ErrOverflow) {
		t.Errorf("max+1: %v", err)
	}
	if _, err := minV.Sub(one); !errors.Is(err, ErrOverflow) {
		t.Errorf("min-1: %v", err)
	}
	if _, err := minV.Add(must(one.Neg())); !errors.Is(err, ErrOverflow) {
		t.Errorf("min+(-1): %v", err)
	}
	if _, err := maxV.Sub(must(one.Neg())); !errors.Is(err, ErrOverflow) {
		t.Errorf("max-(-1): %v", err)
	}
	if _, err := minV.Neg(); !errors.Is(err, ErrOverflow) {
		t.Errorf("-min: %v", err)
	}
	if got := must(maxV.Neg()); got.MinorUnits() != -math.MaxInt64 {
		t.Errorf("-max = %d", got.MinorUnits())
	}
	// limites exatos não são overflow
	if _, err := maxV.Sub(maxV); err != nil {
		t.Errorf("max-max: %v", err)
	}
	if _, err := must(maxV.Neg()).Sub(one); err != nil {
		t.Errorf("-max-1 = min: %v", err)
	}
}

func TestCurrencyMismatch(t *testing.T) {
	brl := must(Parse("10.00", "BRL"))
	usd := must(Parse("10.00", "USD"))
	if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Add: %v", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sub: %v", err)
	}
	if _, err := brl.Cmp(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Cmp: %v", err)
	}
	if brl == usd {
		t.Error("same amount in different currencies must not be equal")
	}
}

func TestUninitialized(t *testing.T) {
	var zero Money
	ok := must(Parse("1.00", "BRL"))
	if zero.Valid() || zero.IsZero() || zero.IsPositive() || zero.IsNegative() {
		t.Error("Money{} must be invalid")
	}
	checks := map[string]error{}
	_, checks["Add"] = zero.Add(ok)
	_, checks["Add2"] = ok.Add(zero)
	_, checks["Sub"] = zero.Sub(ok)
	_, checks["Neg"] = zero.Neg()
	_, checks["Cmp"] = ok.Cmp(zero)
	_, checks["Marshal"] = json.Marshal(zero)
	_, checks["FromMinorUnits"] = FromMinorUnits(1, Currency{})
	_, checks["Zero"] = Zero(Currency{})
	for name, err := range checks {
		if !errors.Is(err, ErrUninitialized) {
			t.Errorf("%s with uninitialized value: %v", name, err)
		}
	}
}

func TestZero(t *testing.T) {
	z := must(Zero(EUR))
	if !z.IsZero() || z.Amount() != "0.00" || z.Currency() != EUR {
		t.Errorf("Zero(EUR) = %v", z)
	}
}

func TestJSON(t *testing.T) {
	m := must(Parse("25.00", "BRL"))
	b, err := json.Marshal(m)
	if err != nil || string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var back Money
	if err := json.Unmarshal(b, &back); err != nil || back != m {
		t.Fatalf("Unmarshal = %v, %v", back, err)
	}
	neg := must(ParseSigned("-5.00", "BRL"))
	if b, _ := json.Marshal(neg); string(b) != `{"amount":"-5.00","currency":"BRL"}` {
		t.Errorf("negative Marshal = %s", b)
	}
}

func TestUnmarshalRejects(t *testing.T) {
	cases := map[string]error{
		`{"amount":25.00,"currency":"BRL"}`:                    ErrInvalidAmount, // número JSON
		`{"amount":2.5e1,"currency":"BRL"}`:                    ErrInvalidAmount,
		`{"amount":"25.00"}`:                                   ErrInvalidAmount,
		`{"currency":"BRL"}`:                                   ErrInvalidAmount,
		`{"amount":"25.00","currency":"BRL","extra":1}`:        ErrInvalidAmount,
		`{"Amount":"25.00","currency":"BRL"}`:                  ErrInvalidAmount, // grafia exata
		`{"amount":null,"currency":"BRL"}`:                     ErrInvalidAmount,
		`{"amount":"-25.00","currency":"BRL"}`:                 ErrNegativeAmount,
		`{"amount":"25.000","currency":"BRL"}`:                 ErrInvalidScale,
		`{"amount":"25.00","currency":"brl"}`:                  ErrUnsupportedCurrency,
		`{"amount":"25.00","currency":1}`:                      ErrUnsupportedCurrency,
		`{"amount":"1.00","amount":"999.00","currency":"BRL"}`: ErrInvalidAmount, // chave repetida (VULN-005)
		`{"amount":"1.00","AMOUNT":"999.00","currency":"BRL"}`: ErrInvalidAmount,
		`null`:    ErrInvalidAmount,
		`"25.00"`: ErrInvalidAmount,
		`[]`:      ErrInvalidAmount,
	}
	for in, want := range cases {
		var m Money
		if err := json.Unmarshal([]byte(in), &m); !errors.Is(err, want) {
			t.Errorf("Unmarshal(%s) error = %v, want %v", in, err, want)
		}
		if m.Valid() {
			t.Errorf("Unmarshal(%s) must leave value untouched", in)
		}
	}
}

func FuzzParseNeverPanicsAndRoundTrips(f *testing.F) {
	for _, s := range []string{"0.00", "25.00", "92233720368547758.07", "1e3", "-1.00", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := Parse(s, "BRL")
		if err != nil {
			return
		}
		if m.Amount() != s {
			t.Fatalf("accepted %q but formats as %q (non-canonical input accepted)", s, m.Amount())
		}
		if m.IsNegative() {
			t.Fatalf("accepted negative %q", s)
		}
	})
}

// VULN-006: entradas enormes não são ecoadas inteiras em mensagens de erro.
func TestErrorsDoNotEchoUnboundedInput(t *testing.T) {
	huge := strings.Repeat("9", 1<<20) + ".00"
	for name, err := range map[string]error{
		"amount":   func() error { _, err := Parse(huge, "BRL"); return err }(),
		"signed":   func() error { _, err := ParseSigned("-"+huge, "BRL"); return err }(),
		"currency": func() error { _, err := ParseCurrency(huge); return err }(),
		"negative": func() error { _, err := Parse("-"+huge, "BRL"); return err }(),
	} {
		if err == nil || len(err.Error()) > 200 {
			t.Errorf("%s: error length %d", name, len(err.Error()))
		}
	}
	if got := Clip("abc"); got != `"abc"` {
		t.Errorf("Clip short = %s", got)
	}
	if got := Clip(strings.Repeat("x", 40)); !strings.HasSuffix(got, "...(40 bytes)") {
		t.Errorf("Clip long = %s", got)
	}
}
