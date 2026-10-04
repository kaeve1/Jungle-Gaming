package httpapi

import (
	"errors"
	"io"
	"jungle-gaming-backend/internal/adapters/jsonstrict"
	"mime"
	"net/http"
)

var errJSON = errors.New("invalid JSON body")

// Token traversal detects duplicate keys at every level without decoding any
// number as float. The destination and exact top-level allowlist validate types.
func strictJSON(w http.ResponseWriter, r *http.Request, dst any, allowed ...string) error {
	if len(r.Header.Values("Content-Type")) != 1 {
		return errJSON
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errJSON
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		return err
	}
	return jsonstrict.Decode(data, dst, allowed...)
}
