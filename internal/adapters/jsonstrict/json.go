package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var errJSON = errors.New("invalid JSON")

// Decode rejects duplicate/unknown/aliased keys, excessive nesting and trailing JSON, without floats.
func Decode(data []byte, dst any, allowed ...string) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := walkJSON(d, 0); err != nil {
		return errJSON
	}
	if _, err := d.Token(); err != io.EOF {
		return errJSON
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || obj == nil {
		return errJSON
	}
	for name := range obj {
		found := false
		for _, key := range allowed {
			if name == key {
				found = true
				break
			}
		}
		if !found {
			return errJSON
		}
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}
func walkJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errJSON
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errJSON
			}
			seen[name] = true
			if err = walkJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return errJSON
		}
	case '[':
		for d.More() {
			if err = walkJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return errJSON
		}
	default:
		return errJSON
	}
	return nil
}
