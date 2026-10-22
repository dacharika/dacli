package nativeconfig

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
)

type growingPrivateReader struct{ consumed int }

func (r *growingPrivateReader) Read(p []byte) (int, error) {
	// More bytes arrive after the apparent boundary, as with a growing file.
	// Keep the source finite so a disabled-limit mutation fails without hanging.
	if r.consumed == 1024 {
		return 0, io.EOF
	}
	p = p[:min(len(p), 1024-r.consumed)]
	for i := range p {
		p[i] = 'x'
	}
	r.consumed += len(p)
	return len(p), nil
}

type failedPrivateReader struct{}

func (failedPrivateReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestPrivateReaderExactBoundaryAndGrowingInput(t *testing.T) {
	const limit int64 = 8
	raw, err := readPrivateBytes(bytes.NewReader([]byte("12345678")), limit)
	if err != nil || string(raw) != "12345678" {
		t.Fatalf("exact boundary: %q, %v", raw, err)
	}
	growing := &growingPrivateReader{}
	raw, err = readPrivateBytes(growing, limit)
	if !errors.Is(err, ErrDenied) || raw != nil {
		t.Fatalf("growing input accepted: %q, %v", raw, err)
	}
	if growing.consumed != int(limit+1) {
		t.Fatalf("consumed %d bytes, want exactly limit+1 (%d)", growing.consumed, limit+1)
	}
}

func TestPrivateReaderRefusesErrorsAndInvalidLimits(t *testing.T) {
	if raw, err := readPrivateBytes(failedPrivateReader{}, 8); !errors.Is(err, ErrDenied) || raw != nil {
		t.Fatalf("read failure not sanitized: %q, %v", raw, err)
	}
	for _, limit := range []int64{-1, math.MaxInt64} {
		r := &growingPrivateReader{}
		if raw, err := readPrivateBytes(r, limit); !errors.Is(err, ErrDenied) || raw != nil || r.consumed != 0 {
			t.Fatalf("invalid limit %d read data: %v, %v, %d", limit, raw, err, r.consumed)
		}
	}
}
