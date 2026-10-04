package postgres

import (
	"encoding/base64"
	"jungle-gaming-backend/internal/domain"
	"testing"
)

func TestCursorRejectsOtherWalletAndAmbiguousEncoding(t *testing.T) {
	wallet, _ := domain.ParseID("0192f291-27dd-7d3f-8071-5f8685deef37")
	valid := `{"w":"0192f291-27dd-7d3f-8071-5f8685deef37","v":1,"i":"0192f291-27dd-7d3f-8071-5f8685deef38"}`
	if _, err := decodeCursor(base64.RawURLEncoding.EncodeToString([]byte(valid)), wallet); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"w":"other-wallet","v":1,"i":"0192f291-27dd-7d3f-8071-5f8685deef38"}`,
		`{"w":"0192f291-27dd-7d3f-8071-5f8685deef37","v":1,"v":2,"i":"0192f291-27dd-7d3f-8071-5f8685deef38"}`,
		`{"w":"0192f291-27dd-7d3f-8071-5f8685deef37","v":0,"i":"0192f291-27dd-7d3f-8071-5f8685deef38"}`,
		valid[:len(valid)-1] + `,"unknown":true}`,
	} {
		if _, err := decodeCursor(base64.RawURLEncoding.EncodeToString([]byte(raw)), wallet); err == nil {
			t.Fatal("ambiguous cursor accepted")
		}
	}
}
