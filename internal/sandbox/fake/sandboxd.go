package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandboxd"
)

// RunSandboxd makes every started sandbox run an in-process sandboxd that
// dials ws://<host>:<boot.TunnelPort>/tunnel, with the sandbox's home
// directory as its home. Terminals then run on the machine running the
// server, as the server's user: this is for tests and development only.
func (d *Driver) RunSandboxd(host string, log *slog.Logger) {
	var mu sync.Mutex
	running := map[sandbox.ID]context.CancelFunc{}
	stop := func(id sandbox.ID) {
		mu.Lock()
		defer mu.Unlock()
		if cancel, ok := running[id]; ok {
			cancel()
			delete(running, id)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.OnStart = func(id sandbox.ID, sb Sandbox, boot sandbox.Boot) {
		stop(id)
		dmn, err := sandboxd.New(sandboxd.Config{
			Boot: sandboxd.Boot{
				AgentID: boot.AgentID, Token: boot.Token,
				TunnelURL: fmt.Sprintf("ws://%s:%d/tunnel", host, boot.TunnelPort),
			},
			Home: sb.HomeDir, Version: "fake", Logger: log,
			// Unix socket paths are short (about 104 bytes on macOS).
			PlatformSocket: filepath.Join(os.TempDir(), "ah-"+shortHash(string(id))+".sock"),
		})
		if err != nil {
			if log != nil {
				log.Error("fake sandboxd", "err", err)
			}
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		mu.Lock()
		running[id] = cancel
		mu.Unlock()
		go func() { _ = dmn.Run(ctx) }()
	}
	d.OnStop = stop
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}
