package nativegovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// observeNativeLandingPR is the transport adapter for the explicit owner CLI.
// Application code receives typed observations and never starts a process.
func observeNativeLandingPR(ctx context.Context, repository string, number int) (application.NativeLandingPRObservation, error) {
	var result application.NativeLandingPRObservation
	if ctx == nil || number < 1 || number > 1_000_000_000 || repository == "" {
		return result, sessions.ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(number), "--repo", repository, "--json", "number,url,state,headRefOid,baseRefName,mergeCommit")
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Env = append([]string(nil), os.Environ()...)
	var stdout, stderr nativeLandingOutput
	stdout.limit, stderr.limit = 1<<20, 1<<20
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || stdout.exceeded || stderr.exceeded || ctx.Err() != nil {
		return result, sessions.ErrDenied
	}
	var wire struct {
		Number      int    `json:"number"`
		URL         string `json:"url"`
		State       string `json:"state"`
		HeadRefOID  string `json:"headRefOid"`
		BaseRefName string `json:"baseRefName"`
		MergeCommit struct {
			OID             string `json:"oid"`
			URL             string `json:"url"`
			MessageHeadline string `json:"messageHeadline"`
			MessageBody     string `json:"messageBody"`
		} `json:"mergeCommit"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return result, sessions.ErrDenied
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return result, sessions.ErrDenied
	}
	return application.NativeLandingPRObservation{Number: wire.Number, URL: wire.URL, State: wire.State, HeadRefOID: wire.HeadRefOID, BaseRefName: wire.BaseRefName, MergeCommitOID: wire.MergeCommit.OID}, nil
}

type nativeLandingOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *nativeLandingOutput) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		return 0, errors.New("output limit unavailable")
	}
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	if len(data) > remaining {
		_, _ = b.buffer.Write(data[:remaining])
		b.exceeded = true
		return remaining, errors.New("output limit exceeded")
	}
	return b.buffer.Write(data)
}
