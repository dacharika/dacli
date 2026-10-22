package sessions

import (
	"os"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
)

// MaxAuthorityBytes bounds the complete authority aggregate, matching the existing
// 32 MiB storage observation boundary. Reaching it requires owner intervention;
// permanent replay, history and consumption records are never pruned (issue #1091).
const MaxAuthorityBytes int64 = 32 << 20

func readAuthority(path string, create bool) ([]byte, error) {
	if create {
		// ReadPrivate deliberately sanitizes absence to ErrDenied. Only an actual
		// missing directory entry permits create-once bootstrap under the lock.
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, ErrStorage
		}
		return nil, ErrDenied
	}
	data, err := nativeconfig.ReadPrivate(path, MaxAuthorityBytes)
	if err != nil {
		return nil, ErrStorage
	}
	return data, nil
}
