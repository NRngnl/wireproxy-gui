package localforward

import (
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/tcpip/ports"
)

type slotBudget struct {
	limit int64
	used  atomic.Int64
}

func (b *slotBudget) tryAcquire() bool {
	for {
		cur := b.used.Load()
		if cur >= b.limit {
			return false
		}
		if b.used.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (b *slotBudget) release() {
	b.used.Add(-1)
}

func gvisorEphemeralPorts() int {
	first, last := ports.NewPortManager().PortRange()
	if last < first {
		return 0
	}
	return int(last) - int(first) + 1
}

func derivePortLimits(ephemeralPorts, fdLimit int) (tcpLimit, udpLimit int) {
	if ephemeralPorts <= 0 {
		ephemeralPorts = gvisorEphemeralPorts()
	}
	udpLimit = max(1, ephemeralPorts/2)
	tcpLimit = max(1, ephemeralPorts/4)
	if fdLimit > 0 {
		tcpLimit = min(tcpLimit, max(1, fdLimit/4))
	}
	return tcpLimit, udpLimit
}
