package domain

import (
	"errors"
	"testing"
)

func TestClassifyDuplicateRejectsUninitializedInputs(t *testing.T) {
	ids := &seqIDs{}
	req, err := NewExternalRequest(raw(KindBet, "boundary", brl("1.00"), ""))
	if err != nil {
		t.Fatal(err)
	}
	tx := newTx(t, ids, raw(KindBet, "boundary", brl("1.00"), ""))
	for _, tc := range []struct {
		name string
		tx   *WagerTransaction
		req  ExternalRequest
	}{
		{"nil transaction", nil, req},
		{"zero transaction", &WagerTransaction{}, req},
		{"zero request", tx, ExternalRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if kind, err := ClassifyDuplicate(tc.tx, tc.req); kind != 0 || !errors.Is(err, ErrUninitialized) {
				t.Fatalf("invalid input must be classified without panic: %v, %v", kind, err)
			}
		})
	}
}
