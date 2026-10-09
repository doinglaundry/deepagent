// Package protocol defines the transport-independent input and event contracts.
package protocol

import (
	rand "crypto/rand"
	hex "encoding/hex"
)

func NewID(prefix string) string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
