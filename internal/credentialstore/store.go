// Package credentialstore keeps device credentials exclusively in an OS store.
package credentialstore

import "errors"

var (
	ErrUnsupported = errors.New("native credential storage unavailable on this platform/build")
	ErrMissing     = errors.New("native credential not found")
	ErrDenied      = errors.New("native credential operation refused")
)

const MaxCredentialBytes = 65536

// Store keeps credentials under opaque installation/provider/tenant/device digests, never tokens.
type Store interface {
	Get(string) ([]byte, error)
	Put(string, []byte) error
	Delete(string) error
}

func Open() (Store, error) { return platformStore() }
