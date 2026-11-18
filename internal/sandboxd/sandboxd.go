// Package sandboxd is the daemon that runs inside every sandbox. It dials the
// server, then serves terminals and file access over the tunnel.
package sandboxd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// BootFile is where the server places the boot configuration before each start.
const BootFile = "/etc/agenthold/boot.json"

// Boot is written by the server before every start.
type Boot struct {
	AgentID   string `json:"agentId"`
	Token     string `json:"token"`
	TunnelURL string `json:"tunnelUrl"`
}

// LoadBoot reads the boot file.
func LoadBoot(path string) (Boot, error) {
	var b Boot
	data, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("parse %s: %w", path, err)
	}
	if b.AgentID == "" || b.Token == "" || b.TunnelURL == "" {
		return b, fmt.Errorf("%s is incomplete", path)
	}
	return b, nil
}

// Config configures the daemon.
type Config struct {
	Boot    Boot
	Home    string // default /home/agent
	Version string
	// RunAs is the account terminals run as. Empty means: "agent" if that
	// account exists and we are root, otherwise the current user.
	RunAs  string
	Logger *slog.Logger
	// PlatformSocket overrides the platform socket path (default
	// <home>/.agenthold/platform.sock).
	PlatformSocket string
	// OnShutdown handles a shutdown request from the server (after it was
	// acknowledged). Nil means shutdown requests are refused.
	OnShutdown func()
	// Services are started at boot. When nil they are read from
	// ServicesFile, which is also where runtime changes are saved ("" for
	// neither).
	Services     []tunnel.ServiceSpec
	ServicesFile string
	// BootPath is the file Boot was read from. A resume from a memory
	// snapshot rewrites it with the new token, so a restarted daemon dials
	// with that one ("" for no rewrite).
	BootPath string
	// ResumePort is the vsock port the host announces a resume from a memory
	// snapshot on (0: no listener; only the microVM init child listens).
	ResumePort uint32
}

// Daemon is a running sandboxd.
type Daemon struct {
	cfg      Config
	log      *slog.Logger
	acct     account
	opener   opener
	services *supervisor

	// bootMu guards boot (its token changes when the VM is resumed from a
	// snapshot) and redial, which ends the current tunnel connection.
	bootMu sync.Mutex
	boot   Boot
	redial context.CancelFunc
	// kick wakes the reconnect loop from its backoff after a resume.
	kick chan struct{}
}

type account struct {
	name     string
	uid, gid int
	home     string
	switchTo bool // whether processes must change credentials
}

// New prepares a daemon.
func New(cfg Config) (*Daemon, error) {
	if cfg.Home == "" {
		cfg.Home = "/home/agent"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	acct, err := resolveAccount(cfg.RunAs, cfg.Home)
	if err != nil {
		return nil, err
	}
	d := &Daemon{cfg: cfg, log: cfg.Logger, acct: acct, boot: cfg.Boot, kick: make(chan struct{}, 1)}
	d.services = newSupervisor(d, cfg.ServicesFile)
	d.prepareHome()
	d.installCA()
	return d, nil
}

func resolveAccount(name, home string) (account, error) {
	cur, err := user.Current()
	if err != nil {
		return account{}, err
	}
	if name == "" {
		name = cur.Username
		if os.Geteuid() == 0 {
			if _, err := user.Lookup("agent"); err == nil {
				name = "agent"
			}
		}
	}
	u, err := user.Lookup(name)
	if err != nil {
		return account{}, fmt.Errorf("user %s: %w", name, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return account{name: name, uid: uid, gid: gid, home: home, switchTo: u.Uid != cur.Uid}, nil
}

// prepareHome makes a fresh home volume usable by the agent account.
func (d *Daemon) prepareHome() {
	if err := os.MkdirAll(d.acct.home, 0o755); err != nil {
		d.log.Warn("create home", "err", err)
		return
	}
	if d.acct.switchTo {
		if fi, err := os.Stat(d.acct.home); err == nil && fi.Sys() != nil {
			if err := os.Chown(d.acct.home, d.acct.uid, d.acct.gid); err != nil {
				d.log.Warn("chown home", "err", err)
			}
		}
	}
}

// Run dials the server and serves until ctx ends, reconnecting with backoff.
// Services start first and keep running while the tunnel is down; they stop
// when ctx ends.
func (d *Daemon) Run(ctx context.Context) error {
	specs := d.cfg.Services
	if specs == nil {
		var err error
		if specs, err = loadServices(d.cfg.ServicesFile); err != nil {
			d.log.Warn("services file", "err", err)
		}
	}
	d.services.start(ctx, specs)
	defer d.services.stopAll()
	go d.servePlatform(ctx)
	if d.cfg.ResumePort != 0 {
		go d.serveResume(ctx, d.cfg.ResumePort)
	}
	backoff := 250 * time.Millisecond
	for {
		start := time.Now()
		dctx, cancel := context.WithCancel(ctx)
		d.bootMu.Lock()
		b := d.boot
		d.redial = cancel
		d.bootMu.Unlock()
		err := tunnel.Dial(dctx, b.TunnelURL, b.AgentID, b.Token, d.handle, d.onConnect)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > 30*time.Second {
			backoff = 250 * time.Millisecond
		}
		select {
		case <-d.kick: // resumed: the old connection is dead, dial right away
			d.log.Info("tunnel redial after resume")
			backoff = 250 * time.Millisecond
			continue
		default:
		}
		d.log.Warn("tunnel ended, reconnecting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-d.kick:
			backoff = 250 * time.Millisecond
			continue
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

// setToken replaces the tunnel token and ends the current connection, so
// the reconnect loop dials again at once with the new one.
func (d *Daemon) setToken(token string) {
	d.bootMu.Lock()
	d.boot.Token = token
	redial := d.redial
	d.bootMu.Unlock()
	select {
	case d.kick <- struct{}{}:
	default:
	}
	if redial != nil {
		redial()
	}
}

func (d *Daemon) onConnect(o tunnel.Opener) {
	d.opener.set(o)
	if o != nil {
		d.log.Info("tunnel connected", "url", d.cfg.Boot.TunnelURL)
	}
}

func (d *Daemon) handle(ctx context.Context, st net.Conn, o tunnel.Open) {
	var err error
	switch o.Op {
	case tunnel.OpHello:
		err = tunnel.WriteJSON(st, tunnel.Hello{Version: d.cfg.Version, User: d.acct.name, Home: d.acct.home})
	case tunnel.OpPing:
		err = tunnel.WriteJSON(st, tunnel.OK{OK: true})
	case tunnel.OpShutdown:
		if d.cfg.OnShutdown == nil {
			err = errors.New("shutdown is not supported by this sandbox")
			break
		}
		d.log.Info("shutdown requested by the server")
		if err = tunnel.WriteJSON(st, tunnel.OK{OK: true}); err == nil {
			go d.cfg.OnShutdown()
		}
	case tunnel.OpSuspend:
		// The host pauses and snapshots the VM next: what the guest wrote
		// must be on its disks, which the snapshot keeps as they are.
		d.log.Info("suspend requested by the server")
		syncAll()
		err = tunnel.WriteJSON(st, tunnel.OK{OK: true})
	case tunnel.OpStats:
		err = tunnel.WriteJSON(st, stats())
	case tunnel.OpPTY:
		err = d.pty(ctx, st, o)
	case tunnel.OpHarness:
		err = d.harness(ctx, st, o)
	case tunnel.OpDial:
		err = d.dial(ctx, st, o)
	case tunnel.OpFSList:
		err = d.fsList(st, o)
	case tunnel.OpFSRead:
		err = d.fsRead(st, o)
	case tunnel.OpFSWrite:
		err = d.fsWrite(st, o)
	case tunnel.OpFSMkdir:
		err = d.fsMkdir(st, o)
	case tunnel.OpFSRemove:
		err = d.fsRemove(st, o)
	case tunnel.OpFSTar:
		err = d.fsTar(st, o)
	case tunnel.OpFSUntar:
		err = d.fsUntar(st, o)
	case tunnel.OpFSSymlink:
		err = d.fsSymlink(st, o)
	case tunnel.OpService:
		err = d.service(ctx, st, o)
	case tunnel.OpExec:
		err = d.exec(ctx, st, o)
	default:
		err = fmt.Errorf("unknown operation %q", o.Op)
	}
	if err != nil && !errors.Is(err, net.ErrClosed) {
		_ = tunnel.WriteError(st, err)
	}
}
