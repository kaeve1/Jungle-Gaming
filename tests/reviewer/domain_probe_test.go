//go:build reviewer

package reviewer

import (
	"encoding/json"
	"testing"

	"jungle-gaming-backend/internal/domain/money"
)

// Deliberately calls the public unmarshaler directly as a consumer of this type.
// json.Unmarshal itself rejects trailing data before invoking this method.
func TestReviewerMoneyRejectsTrailingJSON(t *testing.T) {
	for _, input := range []string{
		`{"amount":"1.00","currency":"BRL"} {"amount":"2.00","currency":"BRL"}`,
		`{"amount":"1.00","currency":"BRL"}garbage`,
	} {
		var standard, direct money.Money
		if err := json.Unmarshal([]byte(input), &standard); err == nil {
			t.Fatal("negative control unexpectedly accepted malformed JSON")
		}
		if err := direct.UnmarshalJSON([]byte(input)); err == nil {
			t.Errorf("public Money.UnmarshalJSON accepted trailing data; parsed=%s", direct.Amount())
		}
	}
}
