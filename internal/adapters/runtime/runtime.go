// Package runtime supplies cryptographically random UUIDv7 IDs.
package runtime

import (
	"crypto/rand"
	"time"

	"jungle-gaming-backend/internal/domain"
)

type IDs struct{}

func (IDs) NewID() domain.ID {
	var id domain.ID
	// Go's crypto/rand.Read always fills the buffer or terminates the process.
	_, _ = rand.Read(id[:])
	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		id[i] = byte(ms)
		ms >>= 8
	}
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}
