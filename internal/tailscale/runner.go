package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/wireproxy-gui/internal/connection"
	"example.com/wireproxy-gui/internal/profile"
	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/bufferpool"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

var (
	ErrAlreadyConnected = errors.New("profile is already connected")
	ErrNotTailscale     = errors.New("profile is not a Tailscale profile")
	ErrNotRunning       = errors.New("Tailscale profile is not connected")
	ErrInvalidProfileID = errors.New("invalid Tailscale profile ID")
)

type Event = connection.Event
type EventType = connection.EventType

const (
	EventLog     = connection.EventLog
	EventStarted = connection.EventStarted
	EventStopped = connection.EventStopped
	EventError   = connection.EventError
)

const (
	tailscaleStatusPollInterval    = time.Second
	tailscaleStatusNoticeDelay     = 2 * time.Second
	tailscaleLoginURLPrefix        = "Tailscale login required: open "
	tailscaleLoginURLKeyPrefix     = "login-url|"
	tsnetAuthURLLogPrefix          = "To start this tsnet server, restart with TS_AUTHKEY set, or go to: "
	tsnetAuthLoopLogPrefix         = "AuthLoop: state is "
	tsnetAuthKeyIgnoredLogPrefix   = "Authkey is set; but state is "
	tailscaleLoginWaitingMessage   = "Tailscale login is waiting for an auth key or browser sign-in URL"
	tailscaleMachineAuthMessage    = "Tailscale login complete; approve this device in the Tailscale admin console. The app will continue automatically after approval"
	tailscaleDeviceApprovedMessage = "Tailscale device approved"
	tailscaleAuthKeyIgnoredMessage = "Tailscale has existing profile state; auth key was not used for this start"
)

type tailscaleAuthNoticeKind int

const (
	tailscaleAuthNoticePlain tailscaleAuthNoticeKind = iota
	tailscaleAuthNoticeSuppress
	tailscaleAuthNoticeLoginURL
	tailscaleAuthNoticeLoginWaiting
	tailscaleAuthNoticeMachineAuth
	tailscaleAuthNoticeRunning
)

type tailscaleAuthNotice struct {
	message string
	key     string
	kind    tailscaleAuthNoticeKind
}

type tailscaleAuthLogEmitter struct {
	emit func(string)

	mu                  sync.Mutex
	lastAuthKey         string
	pendingKey          string
	pendingSince        time.Time
	machineAuthNotified bool
}

type Runner struct {
	events chan Event

	stateDir string

	listen      func(network, address string) (net.Listener, error)
	newNode     func(profile.Profile, string, func(string, ...any)) (tsNode, error)
	serveSocks5 func(context.Context, tsNode, net.Listener) error

	mu    sync.Mutex
	procs map[string]*process
}

type tsNode interface {
	Up(context.Context) (*ipnstate.Status, error)
	LocalClient() (localClient, error)
	Dial(context.Context, string, string) (net.Conn, error)
	Close() error
}

type localClient interface {
	GetPrefs(context.Context) (*ipn.Prefs, error)
	EditPrefs(context.Context, *ipn.MaskedPrefs) (*ipn.Prefs, error)
	Status(context.Context) (*ipnstate.Status, error)
}

type realNode struct {
	server *tsnet.Server
}

func (n *realNode) Up(ctx context.Context) (*ipnstate.Status, error) {
	return n.server.Up(ctx)
}

func (n *realNode) LocalClient() (localClient, error) {
	return n.server.LocalClient()
}

func (n *realNode) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return n.server.Dial(ctx, network, address)
}

func (n *realNode) Close() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("close embedded Tailscale node: %v", recovered)
		}
	}()
	return n.server.Close()
}

type process struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	doneOnce sync.Once
	closed   bool
	listener net.Listener
	node     tsNode
}

func NewRunner() *Runner {
	stateDir := defaultStateDir()
	r := &Runner{
		events:   make(chan Event, 512),
		stateDir: stateDir,
		listen:   net.Listen,
		procs:    map[string]*process{},
	}
	r.newNode = newTSNetNode
	r.serveSocks5 = r.runSocks5
	return r
}

func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "tailscale")
	}
	return filepath.Join(dir, "wireproxy-gui", "tailscale")
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

func (r *Runner) ExitNodes(ctx context.Context, profileID string) ([]connection.ExitNode, error) {
	r.mu.Lock()
	proc, ok := r.procs[profileID]
	r.mu.Unlock()
	if !ok {
		return nil, ErrNotRunning
	}
	node := proc.getNode()
	if node == nil {
		return nil, ErrNotRunning
	}
	client, err := node.LocalClient()
	if err != nil {
		return nil, err
	}
	status, err := client.Status(ctx)
	if err != nil {
		return nil, err
	}
	return exitNodesFromStatus(status), nil
}

func (r *Runner) UpdateExitNode(ctx context.Context, profileID string, cfg profile.TailscaleConfig) error {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	proc, ok := r.procs[profileID]
	r.mu.Unlock()
	if !ok {
		return ErrNotRunning
	}
	node := proc.getNode()
	if node == nil {
		return ErrNotRunning
	}
	return configureExitNode(ctx, node, cfg, nil)
}

func (r *Runner) Logout(ctx context.Context, profileID string) error {
	profileID = strings.TrimSpace(profileID)
	statePath, err := profileStateDir(r.stateDir, profileID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	_, running := r.procs[profileID]
	r.mu.Unlock()
	if running {
		return ErrAlreadyConnected
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(statePath)
}

func (r *Runner) Start(ctx context.Context, p profile.Profile) error {
	p.Normalize()
	if !p.IsTailscale() {
		return ErrNotTailscale
	}
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
	var node tsNode
	defer func() {
		if started {
			return
		}
		proc.close()
		if node != nil {
			_ = node.Close()
		}
		r.removeProcess(p.ID, proc)
	}()

	listener, err := r.listen("tcp", p.BindAddress())
	if err != nil {
		return fmt.Errorf("listen SOCKS5 on %s: %w", p.BindAddress(), err)
	}
	proc.setListener(listener)

	err = procCtx.Err()
	if err != nil {
		return err
	}

	r.emit(EventLog, p, "starting embedded Tailscale node")
	authLog := newTailscaleAuthLogEmitter(func(message string) {
		r.emit(EventLog, p, message)
	})
	node, err = r.newNode(p, r.stateDir, func(format string, args ...any) {
		authLog.emitUserLog(fmt.Sprintf(format, args...))
	})
	if err != nil {
		return err
	}

	stopAuthMonitor := r.startAuthStateMonitor(procCtx, node, authLog)
	status, err := node.Up(procCtx)
	stopAuthMonitor()
	if err != nil {
		return fmt.Errorf("start embedded Tailscale node: %w", err)
	}

	err = procCtx.Err()
	if err != nil {
		return err
	}
	authLog.emitDeviceApprovedIfNeeded()

	err = configureExitNode(procCtx, node, p.TailscaleConfig, status)
	if err != nil {
		return fmt.Errorf("configure Tailscale exit node: %w", err)
	}

	if !proc.setNode(node) {
		node = nil
		return context.Canceled
	}
	runningNode := node
	node = nil

	if !proc.commitStart() {
		return context.Canceled
	}
	started = true
	r.emit(EventStarted, p, "connected on "+p.BindAddress())

	go func() {
		err := r.serveSocks5(procCtx, runningNode, listener)
		canceled := procCtx.Err() != nil
		proc.close()
		r.removeProcess(p.ID, proc)

		switch {
		case canceled:
			r.emit(EventStopped, p, "disconnected")
		case err != nil && !errors.Is(err, net.ErrClosed):
			r.emit(EventError, p, err.Error())
		default:
			r.emit(EventStopped, p, "embedded Tailscale node exited")
		}
	}()

	return nil
}

func newTailscaleAuthLogEmitter(emit func(string)) *tailscaleAuthLogEmitter {
	return &tailscaleAuthLogEmitter{emit: emit}
}

func (l *tailscaleAuthLogEmitter) emitUserLog(text string) {
	notice := tailscaleUserLogNotice(text)
	if notice.kind == tailscaleAuthNoticeSuppress || notice.message == "" {
		return
	}
	if notice.kind == tailscaleAuthNoticePlain {
		l.emit(notice.message)
		return
	}
	l.emitNoticeAt(notice, time.Now())
}

func (l *tailscaleAuthLogEmitter) emitStatus(status *ipnstate.Status) {
	notice := tailscaleAuthStateNotice(status)
	if notice.message == "" {
		return
	}
	l.emitNoticeAt(notice, time.Now())
}

func (l *tailscaleAuthLogEmitter) emitDeviceApprovedIfNeeded() {
	l.emitNoticeAt(tailscaleAuthNotice{
		message: tailscaleDeviceApprovedMessage,
		key:     ipn.Running.String(),
		kind:    tailscaleAuthNoticeRunning,
	}, time.Now())
}

func (l *tailscaleAuthLogEmitter) emitNoticeAt(notice tailscaleAuthNotice, now time.Time) {
	if notice.message == "" || notice.kind == tailscaleAuthNoticeSuppress {
		return
	}
	if notice.kind == tailscaleAuthNoticePlain {
		l.emit(notice.message)
		return
	}

	l.mu.Lock()
	if notice.kind != tailscaleAuthNoticeLoginWaiting && notice.kind != tailscaleAuthNoticeMachineAuth {
		l.pendingKey = ""
		l.pendingSince = time.Time{}
	}
	if notice.key != "" && notice.key == l.lastAuthKey {
		l.mu.Unlock()
		return
	}
	switch notice.kind {
	case tailscaleAuthNoticeLoginWaiting, tailscaleAuthNoticeMachineAuth:
		if l.pendingKey != notice.key {
			l.pendingKey = notice.key
			l.pendingSince = now
			l.mu.Unlock()
			return
		}
		if now.Sub(l.pendingSince) < tailscaleStatusNoticeDelay {
			l.mu.Unlock()
			return
		}
	case tailscaleAuthNoticeRunning:
		if !l.machineAuthNotified {
			l.mu.Unlock()
			return
		}
	}
	if notice.kind == tailscaleAuthNoticeMachineAuth {
		l.machineAuthNotified = true
	}
	if notice.kind == tailscaleAuthNoticeRunning {
		l.machineAuthNotified = false
	}
	l.lastAuthKey = notice.key
	message := notice.message
	l.mu.Unlock()

	l.emit(message)
}

func (r *Runner) startAuthStateMonitor(ctx context.Context, node tsNode, authLog *tailscaleAuthLogEmitter) context.CancelFunc {
	monitorCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(tailscaleStatusPollInterval)
		defer ticker.Stop()

		var client localClient
		for {
			if monitorCtx.Err() != nil {
				return
			}
			if client == nil {
				c, err := node.LocalClient()
				if err == nil {
					client = c
				}
			}
			if client != nil {
				status, err := client.Status(monitorCtx)
				if err == nil {
					authLog.emitStatus(status)
				}
			}

			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func tailscaleAuthStateNotice(status *ipnstate.Status) tailscaleAuthNotice {
	if status == nil {
		return tailscaleAuthNotice{}
	}
	state := strings.TrimSpace(status.BackendState)
	switch state {
	case ipn.NeedsLogin.String(), ipn.NoState.String():
		if authURL := strings.TrimSpace(status.AuthURL); authURL != "" {
			return tailscaleAuthNotice{
				message: tailscaleLoginURLPrefix + authURL,
				key:     tailscaleLoginURLKeyPrefix + authURL,
				kind:    tailscaleAuthNoticeLoginURL,
			}
		}
		return tailscaleAuthNotice{
			message: tailscaleLoginWaitingMessage,
			key:     state,
			kind:    tailscaleAuthNoticeLoginWaiting,
		}
	case ipn.NeedsMachineAuth.String():
		return tailscaleAuthNotice{
			message: tailscaleMachineAuthMessage,
			key:     state,
			kind:    tailscaleAuthNoticeMachineAuth,
		}
	case ipn.Running.String():
		return tailscaleAuthNotice{
			message: tailscaleDeviceApprovedMessage,
			key:     state,
			kind:    tailscaleAuthNoticeRunning,
		}
	default:
		return tailscaleAuthNotice{key: state}
	}
}

func tailscaleAuthStateMessage(status *ipnstate.Status) (message, key string) {
	notice := tailscaleAuthStateNotice(status)
	return notice.message, notice.key
}

func tailscaleUserLogNotice(text string) tailscaleAuthNotice {
	text = strings.TrimSpace(text)
	if authURL, ok := strings.CutPrefix(text, tsnetAuthURLLogPrefix); ok {
		authURL = strings.TrimSpace(authURL)
		if authURL != "" {
			return tailscaleAuthNotice{
				message: tailscaleLoginURLPrefix + authURL,
				key:     tailscaleLoginURLKeyPrefix + authURL,
				kind:    tailscaleAuthNoticeLoginURL,
			}
		}
	}
	if strings.HasPrefix(text, tsnetAuthLoopLogPrefix) {
		return tailscaleAuthNotice{kind: tailscaleAuthNoticeSuppress}
	}
	if strings.HasPrefix(text, tsnetAuthKeyIgnoredLogPrefix) {
		return tailscaleAuthNotice{
			message: tailscaleAuthKeyIgnoredMessage,
			key:     "auth-key-ignored",
			kind:    tailscaleAuthNoticeLoginURL,
		}
	}
	return tailscaleAuthNotice{
		message: text,
		kind:    tailscaleAuthNoticePlain,
	}
}

func tailscaleUserLogMessage(text string) string {
	return tailscaleUserLogNotice(text).message
}

func configureExitNode(ctx context.Context, node tsNode, cfg profile.TailscaleConfig, status *ipnstate.Status) error {
	client, err := node.LocalClient()
	if err != nil {
		return err
	}
	if status == nil {
		status, err = client.Status(ctx)
		if err != nil {
			return err
		}
	}
	prefs, err := client.GetPrefs(ctx)
	if err != nil {
		return err
	}
	prefs.ClearExitNode()
	prefs.AutoExitNode = ""
	prefs.ExitNodeAllowLANAccess = cfg.ExitNodeAllowLANAccess

	exitNode := strings.TrimSpace(cfg.ExitNode)
	if cfg.AutoExitNode {
		prefs.AutoExitNode = ipn.AnyExitNode
	} else if exitNode != "" {
		if id, ok := exitNodeStableID(exitNode, status); ok {
			prefs.ExitNodeID = id
		} else {
			err = prefs.SetExitNodeIP(exitNode, status)
			if err != nil {
				return err
			}
		}
	}

	_, err = client.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs:                     *prefs,
		ExitNodeIDSet:             true,
		ExitNodeIPSet:             true,
		AutoExitNodeSet:           true,
		ExitNodeAllowLANAccessSet: true,
	})
	return err
}

func exitNodeStableID(exitNode string, status *ipnstate.Status) (tailcfg.StableNodeID, bool) {
	exitNode = strings.TrimSpace(exitNode)
	if exitNode == "" || status == nil {
		return "", false
	}
	for _, peer := range status.Peer {
		if peer == nil || !peer.ExitNodeOption {
			continue
		}
		if string(peer.ID) == exitNode {
			return peer.ID, true
		}
	}
	return "", false
}

func exitNodesFromStatus(status *ipnstate.Status) []connection.ExitNode {
	if status == nil {
		return nil
	}
	nodes := make([]connection.ExitNode, 0, len(status.Peer))
	for _, peer := range status.Peer {
		if peer == nil || !peer.ExitNodeOption || !peer.Online || peer.Location != nil {
			continue
		}
		id := strings.TrimSpace(string(peer.ID))
		if id == "" && len(peer.TailscaleIPs) > 0 {
			id = peer.TailscaleIPs[0].String()
		}
		if id == "" {
			continue
		}
		nodes = append(nodes, connection.ExitNode{
			ID:           id,
			Name:         exitNodeDisplayName(peer),
			Online:       peer.Online,
			TailscaleIPs: exitNodeIPStrings(peer),
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Name == nodes[j].Name {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes
}

func exitNodeDisplayName(peer *ipnstate.PeerStatus) string {
	if peer == nil {
		return ""
	}
	name := strings.TrimSuffix(strings.TrimSpace(peer.DNSName), ".")
	if before, _, ok := strings.Cut(name, "."); ok {
		name = before
	}
	if name == "" {
		name = strings.TrimSpace(peer.HostName)
	}
	if name == "" {
		name = strings.TrimSpace(string(peer.ID))
	}
	if name == "" && len(peer.TailscaleIPs) > 0 {
		name = peer.TailscaleIPs[0].String()
	}
	return name
}

func exitNodeIPStrings(peer *ipnstate.PeerStatus) []string {
	if peer == nil || len(peer.TailscaleIPs) == 0 {
		return nil
	}
	ips := make([]string, 0, len(peer.TailscaleIPs))
	for _, ip := range peer.TailscaleIPs {
		ips = append(ips, ip.String())
	}
	return ips
}

func newTSNetNode(p profile.Profile, stateDir string, userLogf func(string, ...any)) (tsNode, error) {
	err := os.MkdirAll(stateDir, 0o700)
	if err != nil {
		return nil, err
	}
	nodeDir, err := profileStateDir(stateDir, p.ID)
	if err != nil {
		return nil, err
	}
	cfg := p.TailscaleConfig
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = p.Name
	}
	server := &tsnet.Server{
		Dir:        nodeDir,
		Hostname:   hostname,
		AuthKey:    cfg.AuthKey,
		ControlURL: cfg.ControlURL,
		Ephemeral:  cfg.Ephemeral,
		UserLogf:   userLogf,
	}
	return &realNode{server: server}, nil
}

func profileStateDir(stateDir, profileID string) (string, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || profileID == "." || profileID == ".." || strings.ContainsAny(profileID, `/\`) {
		return "", ErrInvalidProfileID
	}
	return filepath.Join(stateDir, profileID), nil
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

func (r *Runner) runSocks5(_ context.Context, node tsNode, listener net.Listener) error {
	server := socks5.NewServer(
		socks5.WithResolver(noResolve{}),
		socks5.WithDialAndRequest(func(ctx context.Context, network, _ string, request *socks5.Request) (net.Conn, error) {
			conn, err := node.Dial(ctx, network, request.RawDestAddr.String())
			if err != nil {
				return nil, err
			}
			return socksReplyConn{Conn: conn}, nil
		}),
		socks5.WithBufferPool(bufferpool.NewPool(256*1024)),
	)
	return server.Serve(listener)
}

type socksReplyConn struct {
	net.Conn
}

func (c socksReplyConn) LocalAddr() net.Addr {
	addr := c.Conn.LocalAddr()
	switch addr.(type) {
	case *net.TCPAddr, *net.UDPAddr:
		return addr
	default:
		return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
	}
}

type noResolve struct{}

func (noResolve) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
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

func (p *process) setNode(node tsNode) bool {
	p.mu.Lock()
	closed := p.closed
	if !closed {
		p.node = node
	}
	p.mu.Unlock()
	if closed {
		if node != nil {
			_ = node.Close()
		}
		return false
	}
	return true
}

func (p *process) commitStart() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed
}

func (p *process) getNode() tsNode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.node
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
	node := p.node
	p.mu.Unlock()

	cancel()
	if listener != nil {
		_ = listener.Close()
	}
	if node != nil {
		_ = node.Close()
	}
}

func (r *Runner) removeProcess(profileID string, proc *process) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.procs[profileID] == proc {
		delete(r.procs, profileID)
		proc.markDone()
	}
}

func (r *Runner) emit(eventType EventType, p profile.Profile, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
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
