//go:build !darwin && (linux || freebsd || openbsd || netbsd || dragonfly)

package store

// Other Unix systems retain the existing fail-closed cleanup behavior. Darwin's
// terminal-zombie process-group observation is not portable to these kernels.
func verificationGroupCleanupSafeAfterEPERM(int, int) bool { return false }
