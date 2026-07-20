package cnpg

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

const ownershipIDBytes = 16

func NewOwnershipID() (string, error) {
	return newOwnershipID(rand.Reader)
}

func newOwnershipID(reader io.Reader) (string, error) {
	data := make([]byte, ownershipIDBytes)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", fmt.Errorf("read random ownership id: %w", err)
	}
	return hex.EncodeToString(data), nil
}
