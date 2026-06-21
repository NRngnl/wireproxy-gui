package wireproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/bufferpool"
	upstream "github.com/windtf/wireproxy"
	"golang.zx2c4.com/wireguard/device"
)

var (
	ErrAlreadyConnected = errors.New("profile is already connected")
	ErrConfigInvalid    = errors.New("wireproxy config validation failed")
	ErrSocks5Missing    = errors.New("wireproxy config does not contain a SOCKS5 listener")
)

type EventType = connection.EventType

const (
	EventLog     = connection.EventLog
	EventStarted = connection.EventStarted
	EventStopped = connection.EventStopped
	EventError   = connection.EventError
)

type Event = connection.Event

type Runner struct {
	events chan Event

	parseConfig    func(profile.Profile) (*upstream.Configuration, error)
	startWireguard func(*upstream.Configuration, int) (*upstream.VirtualTun, error)
	serveSocks5    func(context.Context, *upstream.Socks5Config, *upstream.VirtualTun, net.Listener) error
	listen         func(network, address string) (net.Listener, error)

	mu    sync.Mutex
	procs map[string]*process
}

type process struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	doneOnce sync.Once
	closed   bool
	listener net.Listener
	tun      *upstream.VirtualTun
}

func NewRunner() *Runner {
	r := &Runner{
		events:         make(chan Event, 512),
		procs:          map[string]*process{},
		parseConfig:    parseProfileConfig,
		startWireguard: upstream.StartWireguard,
		listen:         net.Listen,
	}
	r.serveSocks5 = r.runSocks5
	return r
}

func (r *Runner) Events() <-chan Event {
	return r.events
}

func (r *Runner) Running(profileID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.procs[profileID]
	return ok
}

func (r *Runner) ExitNodes(context.Context, string) ([]connection.ExitNode, error) {
	return nil, connection.ErrExitNodesUnavailable
}

func (r *Runner) UpdateExitNode(context.Context, string, profile.TailscaleConfig) error {
	return connection.ErrExitNodesUnavailable
}

func (r *Runner) Logout(context.Context, string) error {
	return connection.ErrExitNodesUnavailable
}

func (r *Runner) Start(ctx context.Context, p profile.Profile) error {
	p.Normalize()
	err := p.Validate()
	if err != nil {
		return err
	}

	procCtx, cancel := context.WithCancel(ctx)
	proc := newProcess(cancel)

	r.mu.Lock()
	if _, ok := r.procs[p.ID]; ok {
		r.mu.Unlock()
		cancel()
		return fmt.Errorf("%s: %w", p.Name, ErrAlreadyConnected)
	}
	r.procs[p.ID] = proc
	r.mu.Unlock()

	started := false
	defer func() {
		if started {
			return
		}
		proc.close()
		r.removeProcess(p.ID, proc)
	}()

	r.emit(EventLog, p, "validating generated wireproxy configuration")
	conf, err := r.parseConfig(p)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConfigInvalid, err)
	}
	socksConfig, err := findSocks5Config(conf)
	if err != nil {
		return err
	}

	listener, err := r.listen("tcp", socksConfig.BindAddress)
	if err != nil {
		return fmt.Errorf("listen SOCKS5 on %s: %w", socksConfig.BindAddress, err)
	}
	proc.setListener(listener)

	err = procCtx.Err()
	if err != nil {
		return err
	}

	r.emit(EventLog, p, "starting embedded WireGuard engine")
	tun, err := r.startWireguard(conf, device.LogLevelSilent)
	if err != nil {
		return fmt.Errorf("start embedded WireGuard engine: %w", err)
	}
	if !proc.setTun(tun) {
		return context.Canceled
	}

	err = procCtx.Err()
	if err != nil {
		return err
	}

	if !proc.commitStart() {
		return context.Canceled
	}
	started = true
	r.emit(EventStarted, p, "connected on "+p.BindAddress())

	go func() {
		err := r.serveSocks5(procCtx, socksConfig, tun, listener)
		canceled := procCtx.Err() != nil
		proc.close()
		r.removeProcess(p.ID, proc)

		switch {
		case canceled:
			r.emit(EventStopped, p, "disconnected")
		case err != nil && !errors.Is(err, net.ErrClosed):
			r.emit(EventError, p, err.Error())
		default:
			r.emit(EventStopped, p, "embedded WireGuard engine exited")
		}
	}()

	return nil
}

func (r *Runner) Stop(profileID string) bool {
	r.mu.Lock()
	proc, ok := r.procs[profileID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	proc.close()
	return true
}

func (r *Runner) StopAll() {
	procs := r.processes()
	for _, proc := range procs {
		proc.close()
	}
}

func (r *Runner) StopAllAndWait(ctx context.Context) error {
	procs := r.processes()
	for _, proc := range procs {
		proc.close()
	}
	for _, proc := range procs {
		select {
		case <-proc.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (r *Runner) processes() []*process {
	r.mu.Lock()
	procs := make([]*process, 0, len(r.procs))
	for _, proc := range r.procs {
		procs = append(procs, proc)
	}
	r.mu.Unlock()
	return procs
}

func (r *Runner) runSocks5(_ context.Context, config *upstream.Socks5Config, tun *upstream.VirtualTun, listener net.Listener) error {
	authMethods := []socks5.Authenticator{socks5.NoAuthAuthenticator{}}
	if config.Username != "" {
		authMethods = []socks5.Authenticator{
			socks5.UserPassAuthenticator{
				Credentials: socks5.StaticCredentials{config.Username: config.Password},
			},
		}
	}

	server := socks5.NewServer(
		socks5.WithDial(tun.Tnet.DialContext),
		socks5.WithResolver(tun),
		socks5.WithAuthMethods(authMethods),
		socks5.WithBufferPool(bufferpool.NewPool(256*1024)),
	)
	return server.Serve(listener)
}

func (r *Runner) removeProcess(profileID string, proc *process) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.procs[profileID] == proc {
		delete(r.procs, profileID)
		proc.markDone()
	}
}

func newProcess(cancel context.CancelFunc) *process {
	return &process{
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

func (p *process) markDone() {
	p.doneOnce.Do(func() {
		close(p.done)
	})
}

func (p *process) setListener(listener net.Listener) {
	p.mu.Lock()
	closed := p.closed
	if !closed {
		p.listener = listener
	}
	p.mu.Unlock()
	if closed {
		_ = listener.Close()
	}
}

func (p *process) setTun(tun *upstream.VirtualTun) bool {
	p.mu.Lock()
	closed := p.closed
	if !closed {
		p.tun = tun
	}
	p.mu.Unlock()
	if closed {
		closeTun(tun)
		return false
	}
	return true
}

func (p *process) commitStart() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed
}

func (p *process) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	cancel := p.cancel
	listener := p.listener
	tun := p.tun
	p.mu.Unlock()

	cancel()
	if listener != nil {
		_ = listener.Close()
	}
	closeTun(tun)
}

func closeTun(tun *upstream.VirtualTun) {
	if tun != nil && tun.Dev != nil {
		tun.Dev.Close()
	}
}

func parseProfileConfig(p profile.Profile) (*upstream.Configuration, error) {
	tmp, err := os.CreateTemp("", "wireproxy-gui-*.conf")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, err = tmp.WriteString(p.WireproxyConfig())
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	err = tmp.Close()
	if err != nil {
		return nil, err
	}

	return upstream.ParseConfig(tmpPath)
}

func findSocks5Config(conf *upstream.Configuration) (*upstream.Socks5Config, error) {
	for _, spawner := range conf.Routines {
		config, ok := spawner.(*upstream.Socks5Config)
		if ok {
			return config, nil
		}
	}
	return nil, ErrSocks5Missing
}

func (r *Runner) emit(eventType EventType, p profile.Profile, message string) {
	event := connection.Event{
		Type:        eventType,
		ProfileID:   p.ID,
		ProfileName: p.Name,
		Message:     message,
		At:          time.Now(),
	}
	select {
	case r.events <- event:
	default:
	}
}
