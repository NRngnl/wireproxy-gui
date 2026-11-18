package socksconn

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultUDPErrorInterval = 5 * time.Second

type udpOp uint8

const (
	udpOpDial udpOp = iota
	udpOpWrite
	udpOpRedial
	udpOpRetryWrite
	udpOpReplyWrite
	udpOpClientRead
	udpOpTargetRead
	udpOpCount
)

func udpOpLabel(op udpOp) string {
	switch op {
	case udpOpDial:
		return "UDP dial failed"
	case udpOpWrite:
		return "UDP write failed"
	case udpOpRedial:
		return "UDP redial failed"
	case udpOpRetryWrite:
		return "UDP retry write failed"
	case udpOpReplyWrite:
		return "UDP reply write failed"
	case udpOpClientRead:
		return "UDP client read failed"
	case udpOpTargetRead:
		return "UDP target read failed"
	default:
		return "UDP relay failed"
	}
}

type udpFailureError struct {
	op      udpOp
	reason  string
	repeats uint64
}

func (e *udpFailureError) Error() string {
	base := "SOCKS5 " + udpOpLabel(e.op) + ": " + e.reason
	if e.repeats == 1 {
		return base + " (1 repeat)"
	}
	if e.repeats > 1 {
		return base + " (" + strconv.FormatUint(e.repeats, 10) + " repeats)"
	}
	return base
}

func classifyUDPError(err error) string {
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		switch {
		case dnsErr.IsTimeout:
			return "timeout"
		case dnsErr.IsNotFound:
			return "host not found"
		default:
			return "dns lookup failed"
		}
	}

	netErr, ok := errors.AsType[net.Error](err)
	if ok && netErr.Timeout() {
		return "timeout"
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, syscall.ETIMEDOUT):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.ECONNABORTED):
		return "connection aborted"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "host unreachable"
	case errors.Is(err, syscall.ENETDOWN):
		return "network down"
	case errors.Is(err, syscall.EPIPE):
		return "broken pipe"
	case errors.Is(err, syscall.EMSGSIZE):
		return "message too long"
	case errors.Is(err, syscall.ENOBUFS), errors.Is(err, syscall.ENOMEM):
		return "buffer exhausted"
	case errors.Is(err, net.ErrClosed), errors.Is(err, io.ErrClosedPipe):
		return "connection closed"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected EOF"
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "permission denied"
	default:
		if reason, ok := classifyPlatformUDPError(err); ok {
			return reason
		}
		return "unexpected error"
	}
}

type udpErrorKey struct {
	op     udpOp
	reason string
}

type udpOpState struct {
	lastEmit time.Time
	pending  uint64
}

type udpErrorReporter struct {
	onError  func(error)
	interval time.Duration
	now      func() time.Time

	mu            sync.Mutex
	closed        bool
	timer         *time.Timer
	timerDeadline time.Time
	timerGen      uint64
	timerWG       sync.WaitGroup
	ops           map[udpErrorKey]*udpOpState
}

func newUDPErrorReporter(interval time.Duration, onError func(error)) *udpErrorReporter {
	if onError == nil {
		return nil
	}
	if interval <= 0 {
		interval = defaultUDPErrorInterval
	}
	return &udpErrorReporter{
		onError:  onError,
		interval: interval,
		now:      time.Now,
		ops:      make(map[udpErrorKey]*udpOpState),
	}
}

func (r *udpErrorReporter) report(op udpOp, err error) {
	if r == nil || r.onError == nil || err == nil || op >= udpOpCount {
		return
	}
	reason := classifyUDPError(err)
	now := r.now()
	key := udpErrorKey{op: op, reason: reason}

	var toEmit []*udpFailureError

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	st := r.ops[key]
	switch {
	case st == nil:
		r.ops[key] = &udpOpState{lastEmit: now}
		toEmit = append(toEmit, &udpFailureError{
			op:     op,
			reason: reason,
		})
	case st.lastEmit.IsZero() || now.Sub(st.lastEmit) >= r.interval:
		if st.pending > 0 {
			toEmit = append(toEmit, &udpFailureError{
				op:      op,
				reason:  reason,
				repeats: st.pending,
			})
			st.pending = 0
		}
		st.lastEmit = now
		toEmit = append(toEmit, &udpFailureError{
			op:     op,
			reason: reason,
		})
	default:
		st.pending++
		r.armTimerLocked(now)
	}
	r.mu.Unlock()

	r.emitFailures(toEmit)
}

func (r *udpErrorReporter) armTimerLocked(now time.Time) {
	if r.closed {
		return
	}
	earliest, hasPending := r.earliestDeadlineLocked()
	if !hasPending {
		return
	}
	if r.timer != nil {
		if !earliest.Before(r.timerDeadline) {
			return
		}
		if r.timer.Stop() {
			r.timerWG.Done()
		}
		r.timer = nil
		r.timerDeadline = time.Time{}
	}
	delay := max(earliest.Sub(now), 0)
	r.timerGen++
	gen := r.timerGen
	r.timerDeadline = earliest
	r.timerWG.Add(1)
	r.timer = time.AfterFunc(delay, func() {
		defer r.timerWG.Done()
		r.flushScheduled(gen)
	})
}

func (r *udpErrorReporter) earliestDeadlineLocked() (time.Time, bool) {
	var earliest time.Time
	hasPending := false
	for _, st := range r.ops {
		if st.pending == 0 {
			continue
		}
		due := st.lastEmit.Add(r.interval)
		if !hasPending || due.Before(earliest) {
			earliest = due
			hasPending = true
		}
	}
	return earliest, hasPending
}

func (r *udpErrorReporter) flushScheduled(gen uint64) {
	now := r.now()

	r.mu.Lock()
	if r.closed || gen != r.timerGen {
		r.mu.Unlock()
		return
	}
	r.timer = nil
	r.timerDeadline = time.Time{}
	toEmit := r.drainPendingLocked(now, false)
	r.armTimerLocked(now)
	r.mu.Unlock()

	r.emitFailures(toEmit)
}

func (r *udpErrorReporter) close() {
	if r == nil || r.onError == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.timerGen++
	if r.timer != nil {
		if r.timer.Stop() {
			r.timerWG.Done()
		}
		r.timer = nil
		r.timerDeadline = time.Time{}
	}
	r.mu.Unlock()

	r.timerWG.Wait()

	r.mu.Lock()
	toEmit := r.drainPendingLocked(time.Time{}, true)
	r.mu.Unlock()

	r.emitFailures(toEmit)
}

func (r *udpErrorReporter) drainPendingLocked(now time.Time, force bool) []*udpFailureError {
	var toEmit []*udpFailureError
	for key, st := range r.ops {
		if st.pending == 0 {
			if !force && !st.lastEmit.IsZero() && now.Sub(st.lastEmit) >= r.interval {
				delete(r.ops, key)
			}
			continue
		}
		if !force && now.Sub(st.lastEmit) < r.interval {
			continue
		}
		toEmit = append(toEmit, &udpFailureError{
			op:      key.op,
			reason:  key.reason,
			repeats: st.pending,
		})
		if !force {
			delete(r.ops, key)
		} else {
			st.pending = 0
			st.lastEmit = time.Time{}
		}
	}
	slices.SortFunc(toEmit, func(a, b *udpFailureError) int {
		if a.op != b.op {
			return int(a.op) - int(b.op)
		}
		return strings.Compare(a.reason, b.reason)
	})
	return toEmit
}

func (r *udpErrorReporter) emitFailures(failures []*udpFailureError) {
	for _, failure := range failures {
		r.onError(failure)
	}
}
