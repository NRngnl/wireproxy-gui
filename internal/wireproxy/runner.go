package wireproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/bufferpool"
	upstream "github.com/windtf/wireproxy"
	"golang.zx2c4.com/wireguard/device"
)

var (
	ErrAlreadyConnected = connection.ErrAlreadyConnected
	ErrConfigInvalid    = errors.New("wireproxy config validation failed")
	ErrSocks5Missing    = errors.New("wireproxy config does not contain a SOCKS5 listener")

	errInvalidPersistentKeepalive   = errors.New("PersistentKeepalive must be between 0 and 65535")
	errDuplicatePersistentKeepalive = errors.New("duplicate PersistentKeepalive in [Peer]")
)

const defaultPersistentKeepalive = 25

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
	serveSocks5    func(context.Context, profile.Profile, *upstream.Socks5Config, *upstream.VirtualTun, net.Listener) error
	listen         func(network, address string) (net.Listener, error)
	dialTun        func(context.Context, *upstream.VirtualTun, string, string) (net.Conn, error)
	resolveTun     func(context.Context, *upstream.VirtualTun, string) (context.Context, net.IP, error)

	timeouts socksconn.Timeouts

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
		dialTun: func(ctx context.Context, tun *upstream.VirtualTun, network, addr string) (net.Conn, error) {
			return tun.Tnet.DialContext(ctx, network, addr)
		},
		resolveTun: func(ctx context.Context, tun *upstream.VirtualTun, name string) (context.Context, net.IP, error) {
			return tun.Resolve(ctx, name)
		},
		timeouts: socksconn.Timeouts{}.WithDefaults(),
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
		err := r.serveSocks5(procCtx, p, socksConfig, tun, listener)
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

func (r *Runner) runSocks5(ctx context.Context, p profile.Profile, config *upstream.Socks5Config, tun *upstream.VirtualTun, listener net.Listener) error {
	authMethods := []socks5.Authenticator{socks5.NoAuthAuthenticator{}}
	if config.Username != "" {
		authMethods = []socks5.Authenticator{
			socks5.UserPassAuthenticator{
				Credentials: socks5.StaticCredentials{config.Username: config.Password},
			},
		}
	}

	timeouts := r.timeouts.WithDefaults()
	wrappedLn := socksconn.WrapListener(listener, timeouts.TCPIdle, timeouts.Drain)
	stop := context.AfterFunc(ctx, func() {
		_ = wrappedLn.Close()
	})
	defer stop()

	var assocWG sync.WaitGroup
	defer assocWG.Wait()

	dialFn := func(reqCtx context.Context, network, addr string) (net.Conn, error) {
		if resolveCancel, ok := reqCtx.Value(resolveCancelKey{}).(context.CancelFunc); ok {
			defer resolveCancel()
		}
		dialCtx, cancel := socksconn.DialContext(ctx, reqCtx, timeouts.Dial)
		defer cancel()
		conn, err := r.dialTun(dialCtx, tun, network, addr)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(network, "udp") {
			return socksconn.WrapUDP(conn, timeouts.UDPIdle), nil
		}
		return socksconn.WrapTCP(conn, timeouts.TCPIdle, timeouts.Drain), nil
	}

	assocHandle := socksconn.AssociateHandler(ctx, dialFn, func(err error) {
		if err != nil {
			r.emit(EventLog, p, err.Error())
		}
	})

	server := socks5.NewServer(
		socks5.WithDial(dialFn),
		socks5.WithAssociateHandle(func(reqCtx context.Context, writer io.Writer, request *socks5.Request) error {
			if resolveCancel, ok := reqCtx.Value(resolveCancelKey{}).(context.CancelFunc); ok {
				resolveCancel()
			}
			assocWG.Add(1)
			defer assocWG.Done()
			return assocHandle(reqCtx, writer, request)
		}),
		socks5.WithAssociateMiddleware(func(_ context.Context, writer io.Writer, _ *socks5.Request) error {
			if c, ok := writer.(interface{ ClearIdleTimeout() error }); ok {
				return c.ClearIdleTimeout()
			}
			return nil
		}),
		socks5.WithBindMiddleware(func(reqCtx context.Context, _ io.Writer, _ *socks5.Request) error {
			if resolveCancel, ok := reqCtx.Value(resolveCancelKey{}).(context.CancelFunc); ok {
				resolveCancel()
			}
			return nil
		}),
		socks5.WithResolver(boundedResolver{
			parentCtx: ctx,
			timeout:   timeouts.Dial,
			resolve: func(resolveCtx context.Context, name string) (context.Context, net.IP, error) {
				return r.resolveTun(resolveCtx, tun, name)
			},
		}),
		socks5.WithAuthMethods(authMethods),
		socks5.WithBufferPool(bufferpool.NewPool(socksconn.BufferSize)),
	)
	return server.Serve(wrappedLn)
}

type resolveCancelKey struct{}

type boundedResolver struct {
	parentCtx context.Context
	timeout   time.Duration
	resolve   func(context.Context, string) (context.Context, net.IP, error)
}

func (r boundedResolver) Resolve(reqCtx context.Context, name string) (context.Context, net.IP, error) {
	dialCtx, cancel := socksconn.DialContext(r.parentCtx, reqCtx, r.timeout)
	ctxWithCancel := context.WithValue(dialCtx, resolveCancelKey{}, cancel)
	resolvedCtx, ip, err := r.resolve(ctxWithCancel, name)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if resolvedCtx == nil {
		resolvedCtx = ctxWithCancel
	}
	return resolvedCtx, ip, nil
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

	cfgText := wireproxyConfig(p)
	_, err = tmp.WriteString(cfgText)
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	err = tmp.Close()
	if err != nil {
		return nil, err
	}

	conf, err := upstream.ParseConfig(tmpPath)
	if err != nil {
		return nil, err
	}

	wgText := cfgText
	if wgPath, ok := findRootINIKey(cfgText, "WGConfig"); ok {
		if filepath.Base(wgPath) == wgPath {
			wgPath = filepath.Join(filepath.Dir(tmpPath), wgPath)
		}
		wgBytes, readErr := os.ReadFile(wgPath)
		if readErr != nil {
			return nil, readErr
		}
		wgText = string(wgBytes)
	}

	err = applyAndValidatePeerKeepalives(wgText, conf.Device.Peers)
	if err != nil {
		return nil, err
	}
	return conf, nil
}

func wireproxyConfig(p profile.Profile) string {
	base := stripSection(p.WireGuardConfig, "Socks5")
	return fmt.Sprintf("%s\n\n[Socks5]\nBindAddress = %s\n", strings.TrimSpace(base), p.BindAddress())
}

func applyAndValidatePeerKeepalives(wgText string, peers []upstream.PeerConfig) error {
	counts := peerKeepaliveCounts(wgText)
	if len(counts) != len(peers) {
		return errInvalidPersistentKeepalive
	}
	for i, count := range counts {
		switch {
		case count == 0:
			peers[i].KeepAlive = defaultPersistentKeepalive
		case count > 1:
			return fmt.Errorf("%w (peer %d)", errDuplicatePersistentKeepalive, i+1)
		case peers[i].KeepAlive < 0 || peers[i].KeepAlive > 65535:
			return fmt.Errorf("%w: %d", errInvalidPersistentKeepalive, peers[i].KeepAlive)
		}
	}
	return nil
}

func peerKeepaliveCounts(text string) []int {
	var counts []int
	inPeer := false
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if section, ok := sectionHeaderName(trimmed); ok {
			inPeer = section == "peer"
			if inPeer {
				counts = append(counts, 0)
			}
			continue
		}
		if !inPeer {
			continue
		}
		key, _, ok := parseINILine(trimmed)
		if ok && strings.EqualFold(key, "PersistentKeepalive") {
			counts[len(counts)-1]++
		}
	}
	return counts
}

func findRootINIKey(text, targetKey string) (string, bool) {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if _, ok := sectionHeaderName(trimmed); ok {
			break
		}
		key, val, ok := parseINILine(trimmed)
		if ok && strings.EqualFold(key, targetKey) {
			return val, true
		}
	}
	return "", false
}

func parseINILine(trimmed string) (string, string, bool) {
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
		return "", "", false
	}
	idx := strings.IndexAny(trimmed, "=:")
	if idx <= 0 {
		return "", "", false
	}
	key := stripSurroundingQuotes(strings.TrimSpace(trimmed[:idx]))
	val := cleanINIValue(strings.TrimSpace(trimmed[idx+1:]))
	return key, val, key != ""
}

func cleanINIValue(val string) string {
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
		if end := strings.IndexByte(val[1:], val[0]); end >= 0 && strings.TrimSpace(val[end+2:]) == "" {
			return val[1 : end+1]
		}
	}
	if i := strings.IndexAny(val, "#;"); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}
	return stripSurroundingQuotes(val)
}

func stripSurroundingQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'' || s[0] == '`') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func stripSection(text, sectionName string) string {
	target := strings.ToLower(strings.TrimSpace(sectionName))
	kept := make([]string, 0)
	dropping := false
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if current, ok := sectionHeaderName(trimmed); ok {
			dropping = current == target
		}
		if !dropping {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func sectionHeaderName(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "[") || !strings.Contains(trimmed, "]") {
		return "", false
	}
	end := strings.Index(trimmed, "]")
	return strings.ToLower(strings.TrimSpace(trimmed[1:end])), true
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
