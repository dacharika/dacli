//go:build !darwin || !cgo

package credentialstore

func platformStore() (Store, error) { return nil, ErrUnsupported }
