//go:build unix

package localforward

import (
	"math"

	"golang.org/x/sys/unix"
)

func processFDLimit() int {
	var rlim unix.Rlimit
	err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rlim)
	if err != nil || rlim.Cur == 0 || rlim.Cur == unix.RLIM_INFINITY {
		return 0
	}
	if rlim.Cur > math.MaxInt {
		return math.MaxInt
	}
	return int(rlim.Cur)
}
