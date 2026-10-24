//go:build darwin

package store

import (
	"errors"

	"golang.org/x/sys/unix"
)

const (
	darwinZombieProcessState    = 5 // SZOMB from sys/proc.h.
	maxVerificationGroupMembers = 1024
)

type verificationProcessMember struct {
	PID   int
	PGID  int
	State int8
}

// verificationGroupTerminalOnly accepts only a complete observation of one
// exact group whose members have all exited and await reaping. The direct child
// has already been reaped by Cmd.Run, so seeing its PID again makes the group
// identity uncertain and must remain a refusal.
func verificationGroupTerminalOnly(pgid, leaderPID int, members []verificationProcessMember) bool {
	if pgid <= 0 || leaderPID <= 0 || pgid != leaderPID || len(members) > maxVerificationGroupMembers {
		return false
	}
	seen := make(map[int]struct{}, len(members))
	for _, member := range members {
		if member.PID <= 0 || member.PGID != pgid || member.PID == leaderPID || member.State != darwinZombieProcessState {
			return false
		}
		if _, exists := seen[member.PID]; exists {
			return false
		}
		seen[member.PID] = struct{}{}
	}
	return true
}

// verificationGroupCleanupSafeAfterEPERM reads a scoped kernel snapshot. This
// is a cooperative cleanup observation, not containment or proof about escaped
// descendants; every unavailable or ambiguous observation fails closed.
func verificationGroupCleanupSafeAfterEPERM(pgid, leaderPID int) bool {
	return verificationGroupCleanupSafeAfterEPERMSnapshot(pgid, leaderPID, func(groupID int) ([]verificationProcessMember, error) {
		rows, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", groupID)
		if err != nil {
			return nil, err
		}
		if len(rows) > maxVerificationGroupMembers {
			return nil, errVerificationGroupSnapshotTooLarge
		}
		members := make([]verificationProcessMember, 0, len(rows))
		for _, row := range rows {
			members = append(members, verificationProcessMember{
				PID:   int(row.Proc.P_pid),
				PGID:  int(row.Eproc.Pgid),
				State: row.Proc.P_stat,
			})
		}
		return members, nil
	})
}

var errVerificationGroupSnapshotTooLarge = errors.New("verification process group snapshot too large")

// verificationGroupCleanupSafeAfterEPERMSnapshot permits deterministic tests
// of unavailable snapshots without exposing a public process-inspection hook.
func verificationGroupCleanupSafeAfterEPERMSnapshot(
	pgid, leaderPID int,
	snapshot func(int) ([]verificationProcessMember, error),
) bool {
	if pgid <= 0 || leaderPID <= 0 || pgid != leaderPID || snapshot == nil {
		return false
	}
	members, err := snapshot(pgid)
	if err != nil || len(members) > maxVerificationGroupMembers {
		return false
	}
	return verificationGroupTerminalOnly(pgid, leaderPID, members)
}
