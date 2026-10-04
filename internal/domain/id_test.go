package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseID(t *testing.T) {
	id, err := ParseID("0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1")
	if err != nil {
		t.Fatal(err)
	}
	if id.String() != "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1" {
		t.Errorf("uppercase input must be canonicalized to lowercase, got %s", id)
	}
	for _, bad := range []string{
		"", "0192f28f5dc07d58bdb2814ad6a0f4a1", "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a", "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1x",
		"0192f28g-5dc0-7d58-bdb2-814ad6a0f4a1", "{0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1}", "00000000-0000-0000-0000-000000000000",
	} {
		if _, err := ParseID(bad); !errors.Is(err, ErrValidation) {
			t.Errorf("ParseID(%q) error = %v", bad, err)
		}
	}
}

func TestIDJSON(t *testing.T) {
	b, err := json.Marshal(walletA)
	if err != nil || string(b) != `"0192f291-27dd-7d3f-8071-5f8685deef37"` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var back ID
	if err := json.Unmarshal(b, &back); err != nil || back != walletA {
		t.Fatalf("Unmarshal = %v, %v", back, err)
	}
	if _, err := json.Marshal(ID{}); err == nil {
		t.Error("zero ID must not be serialized")
	}
}

func TestParseIDDoesNotEchoUnboundedInput(t *testing.T) {
	_, err := ParseID(strings.Repeat("a", 1<<20))
	if err == nil || len(err.Error()) > 200 {
		t.Errorf("error length %d", len(err.Error()))
	}
}
