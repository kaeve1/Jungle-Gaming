package jsonstrict

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAdversarialObjects(t *testing.T) {
	for _, body := range []string{
		`{"data":{},"data":{}}`,
		`{"data":{"x":1,"x":2}}`,
		`{"data":[{"x":1,"x":2}]}`,
		`{"Data":{}}`,
		`{"unknown":{}}`,
		`{"data":{}} {}`,
		`{"data":{}} true`,
		`null`, `[]`, `{"data":`,
		`{"data":` + strings.Repeat("[", 34) + `0` + strings.Repeat("]", 34) + `}`,
	} {
		t.Run(body, func(t *testing.T) {
			var target struct {
				Data json.RawMessage `json:"data"`
			}
			if Decode([]byte(body), &target, "data") == nil {
				t.Fatal("adversarial object accepted")
			}
		})
	}
	for _, body := range []string{`{}`, `{"data":{"x":9007199254740993}}`, `{"data":{}} `} {
		var target struct {
			Data json.RawMessage `json:"data"`
		}
		if err := Decode([]byte(body), &target, "data"); err != nil {
			t.Fatalf("valid object rejected: %v", err)
		}
	}
}

func FuzzDuplicateAndTrailingObjects(f *testing.F) {
	for _, key := range []string{"data", "d\"ata", "", "😀"} {
		f.Add(key)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if len(key) > 4096 || !utf8.ValidString(key) {
			t.Skip()
		}
		encoded, _ := json.Marshal(key)
		var target map[string]json.RawMessage
		duplicate := "{" + string(encoded) + ":0," + string(encoded) + ":1}"
		if Decode([]byte(duplicate), &target, key) == nil {
			t.Fatal("duplicate key accepted")
		}
		single := "{" + string(encoded) + ":9007199254740993}"
		if Decode([]byte(single), &target, key) != nil {
			t.Fatal("valid object rejected")
		}
		if string(target[key]) != "9007199254740993" {
			t.Fatal("number lost precision")
		}
		if Decode([]byte(single+" {}"), &target, key) == nil {
			t.Fatal("trailing object accepted")
		}
	})
}
