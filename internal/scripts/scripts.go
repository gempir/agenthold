// Package scripts reads a repository's project scripts (agenthold.json, or
// T3 Code's t3.json when there is none) and runs them in an agent's
// machine: on request, when a worktree is created, and when a thread
// settles.
//
// A script runs once to completion (exec) unless it is long-running (it has
// a previewUrl or stops on settle, like a dev server): then it runs as a
// supervised service named after the script and its directory, which
// settling the thread removes again.
package scripts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Files are the configuration files looked for, in this order.
var Files = []string{"agenthold.json", "t3.json"}

// Limits.
const (
	MaxScripts    = 50
	maxConfigSize = 1 << 20
	maxCommand    = 8192
	maxName       = 100
	// ExecTimeout bounds a script that runs to completion (seconds).
	ExecTimeout = 600
	// MaxOutput bounds the output kept of one.
	MaxOutput = 256 << 10
)

// Icons are the icons a script may have ("play" is the default).
var Icons = []string{"play", "test", "lint", "configure", "build", "debug"}

// Environment modes of new threads.
const (
	EnvLocal    = "local"
	EnvWorktree = "worktree"
)

// Script is one project script.
type Script struct {
	// ID is the script's name made a slug (unique within its file).
	ID                  string `json:"-"`
	Name                string `json:"name"`
	Command             string `json:"command"`
	Icon                string `json:"icon,omitempty"`
	RunOnWorktreeCreate bool   `json:"runOnWorktreeCreate,omitempty"`
	// Async (for RunOnWorktreeCreate): true (the default) does not wait for
	// the script to finish.
	Async           *bool  `json:"async,omitempty"`
	StopOnSettle    bool   `json:"stopOnSettle,omitempty"`
	RunOnSettle     bool   `json:"runOnSettle,omitempty"`
	PreviewURL      string `json:"previewUrl,omitempty"`
	AutoOpenPreview bool   `json:"autoOpenPreview,omitempty"`
}

// IsAsync reports whether worktree creation does not wait for the script.
func (s Script) IsAsync() bool { return s.Async == nil || *s.Async }

// LongRunning reports whether the script runs as a supervised service.
func (s Script) LongRunning() bool { return s.PreviewURL != "" || s.StopOnSettle }

// Config is a parsed configuration file.
type Config struct {
	// Source is the file it was read from ("" when there is none).
	Source               string   `json:"-"`
	IconPath             string   `json:"iconPath,omitempty"`
	DefaultThreadEnvMode string   `json:"defaultThreadEnvMode,omitempty"`
	Scripts              []Script `json:"scripts"`
}

// Find returns the script with the given id or name.
func (c Config) Find(ref string) (Script, bool) {
	ref = strings.TrimSpace(ref)
	for _, s := range c.Scripts {
		if s.ID == ref {
			return s, true
		}
	}
	for _, s := range c.Scripts {
		if strings.EqualFold(s.Name, ref) {
			return s, true
		}
	}
	return Script{}, false
}

// Slug makes an id of a script name: lowercase letters, digits and hyphens.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 24 {
		s = strings.Trim(s[:24], "-")
	}
	if s == "" {
		s = "script"
	}
	return s
}

// Parse decodes and checks a configuration file. Unknown fields (T3 Code's
// worktree settings, $schema) are ignored.
func Parse(b []byte) (Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("invalid JSON: %w", err)
	}
	switch c.DefaultThreadEnvMode {
	case "", EnvLocal, EnvWorktree:
	default:
		return Config{}, fmt.Errorf("defaultThreadEnvMode must be %q or %q", EnvLocal, EnvWorktree)
	}
	if len(c.IconPath) > 4096 || strings.ContainsRune(c.IconPath, 0) {
		return Config{}, errors.New("invalid iconPath")
	}
	if len(c.Scripts) > MaxScripts {
		return Config{}, fmt.Errorf("at most %d scripts", MaxScripts)
	}
	seen := map[string]int{}
	for i := range c.Scripts {
		s := &c.Scripts[i]
		s.Name = strings.TrimSpace(s.Name)
		switch {
		case s.Name == "" || len(s.Name) > maxName:
			return Config{}, fmt.Errorf("script %d: a name of up to %d characters is required", i+1, maxName)
		case strings.TrimSpace(s.Command) == "" || len(s.Command) > maxCommand || strings.ContainsRune(s.Command, 0):
			return Config{}, fmt.Errorf("script %s: a command of up to %d bytes is required", s.Name, maxCommand)
		case len(s.PreviewURL) > 2048:
			return Config{}, fmt.Errorf("script %s: previewUrl is too long", s.Name)
		}
		if !validIcon(s.Icon) {
			s.Icon = "play"
		}
		s.ID = Slug(s.Name)
		if n := seen[s.ID]; n > 0 {
			seen[s.ID] = n + 1
			s.ID = fmt.Sprintf("%s-%d", s.ID, n+1)
		} else {
			seen[s.ID] = 1
		}
	}
	if c.Scripts == nil {
		c.Scripts = []Script{}
	}
	return c, nil
}

func validIcon(icon string) bool {
	for _, i := range Icons {
		if i == icon {
			return true
		}
	}
	return false
}

// notFound reports whether err is a missing file in the machine.
func notFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such file or directory")
}

// Load reads the configuration of the directory dir (relative to the home)
// in a machine. A directory without one has an empty configuration.
func Load(ctx context.Context, ts *tunnel.Session, dir string) (Config, error) {
	for _, name := range Files {
		f, err := ts.ReadText(ctx, path.Join(dir, name), maxConfigSize, 0, 0)
		if notFound(err) {
			continue
		}
		if err != nil {
			return Config{}, err
		}
		if f.IsBinary || f.Truncated {
			return Config{}, fmt.Errorf("%s: not a JSON file of up to %d bytes", name, maxConfigSize)
		}
		c, err := Parse([]byte(f.Content))
		if err != nil {
			return Config{Source: name, Scripts: []Script{}}, fmt.Errorf("%s: %w", name, err)
		}
		c.Source = name
		return c, nil
	}
	return Config{Scripts: []Script{}}, nil
}

// ServiceName is the service a long-running script of dir runs as.
func ServiceName(dir string, s Script) string {
	id := s.ID
	if id == "" {
		id = Slug(s.Name)
	}
	if len(id) > 20 {
		id = strings.Trim(id[:20], "-")
	}
	sum := sha256.Sum256([]byte(path.Clean("/" + dir)))
	return "script-" + id + "-" + hex.EncodeToString(sum[:4])
}

// Result is what running a script did.
type Result struct {
	Script Script
	// Service is the service a long-running script runs as ("" for exec).
	Service   string
	ExitCode  int
	Output    string
	Truncated bool
	TimedOut  bool
}

// Runner runs scripts in machines.
type Runner struct {
	// Apps runs long-running scripts as services; without it they run to
	// completion like the others.
	Apps *apps.Manager
	Log  *slog.Logger
}

func (r *Runner) logger() *slog.Logger {
	if r != nil && r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Run runs a script of the directory dir in the agent's machine.
func (r *Runner) Run(ctx context.Context, ts *tunnel.Session, agentID, dir string, s Script) (Result, error) {
	res := Result{Script: s}
	if s.LongRunning() && r != nil && r.Apps != nil {
		spec := apps.Spec{
			Name: ServiceName(dir, s), Command: s.Command, Workdir: dir, Enabled: true,
			Env: map[string]string{"AGENTHOLD_SCRIPT": s.Name},
		}
		changed, _, err := r.Apps.Put(ctx, agentID, spec)
		if err != nil {
			return res, err
		}
		if !changed {
			// Running already: start it afresh.
			if _, err := r.Apps.Restart(ctx, agentID, spec.Name); err != nil {
				return res, err
			}
		}
		res.Service = spec.Name
		return res, nil
	}
	if s.LongRunning() {
		return res, errors.New("long-running scripts (previewUrl, stopOnSettle) need services, which this server does not run")
	}
	out, err := ts.Exec(ctx, []string{"sh", "-lc", s.Command}, dir, map[string]string{"AGENTHOLD_SCRIPT": s.Name}, ExecTimeout, MaxOutput)
	if err != nil {
		return res, err
	}
	res.ExitCode, res.Output, res.Truncated, res.TimedOut = out.ExitCode, out.Output, out.Truncated, out.TimedOut
	return res, nil
}

// OnWorktreeCreate runs the runOnWorktreeCreate scripts of a new worktree
// dir: the ones that are not async before it returns, the others in the
// background. Failures are logged, never returned.
func (r *Runner) OnWorktreeCreate(ctx context.Context, ts *tunnel.Session, agentID, dir string) {
	cfg, err := Load(ctx, ts, dir)
	if err != nil {
		r.logger().Warn("project scripts", "agent", agentID, "dir", dir, "err", err)
		return
	}
	for _, s := range cfg.Scripts {
		if !s.RunOnWorktreeCreate {
			continue
		}
		if !s.IsAsync() || s.LongRunning() {
			r.report(agentID, dir, "worktree create", s)(r.Run(ctx, ts, agentID, dir, s))
			continue
		}
		go func() {
			bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), (ExecTimeout+30)*time.Second)
			defer cancel()
			r.report(agentID, dir, "worktree create", s)(r.Run(bctx, ts, agentID, dir, s))
		}()
	}
}

// settleMu runs settle scripts one at a time.
var settleMu sync.Mutex

// OnSettle runs what settling a thread that works in dirs does: services of
// stopOnSettle scripts are removed, and runOnSettle scripts run in the dirs
// that are linked worktrees (never in main checkouts).
func (r *Runner) OnSettle(ctx context.Context, ts *tunnel.Session, agentID string, dirs []string) {
	for _, dir := range dirs {
		cfg, err := Load(ctx, ts, dir)
		if err != nil {
			r.logger().Warn("project scripts", "agent", agentID, "dir", dir, "err", err)
			continue
		}
		worktree := -1
		for _, s := range cfg.Scripts {
			if s.StopOnSettle && r.Apps != nil {
				name := ServiceName(dir, s)
				if _, err := r.Apps.Get(ctx, agentID, name); err == nil {
					if err := r.Apps.Delete(ctx, agentID, name); err != nil {
						r.logger().Warn("stop a script on settle", "agent", agentID, "service", name, "err", err)
					}
				}
			}
			if !s.RunOnSettle {
				continue
			}
			if worktree < 0 {
				worktree = 0
				if g, err := ts.Git(ctx, tunnel.GitRequest{Action: tunnel.GitStatus, Path: dir}); err == nil && g.Repo != nil && g.Repo.IsWorktree {
					worktree = 1
				}
			}
			if worktree == 1 {
				settleMu.Lock()
				res, err := r.Run(ctx, ts, agentID, dir, Script{ID: s.ID, Name: s.Name, Command: s.Command})
				settleMu.Unlock()
				r.report(agentID, dir, "settle", s)(res, err)
			}
		}
	}
}

func (r *Runner) report(agentID, dir, when string, s Script) func(Result, error) {
	return func(res Result, err error) {
		log := r.logger().With("agent", agentID, "dir", dir, "script", s.Name, "on", when)
		switch {
		case err != nil:
			log.Warn("project script failed to run", "err", err)
		case res.Service != "":
			log.Info("project script started", "service", res.Service)
		case res.ExitCode != 0 || res.TimedOut:
			tail := res.Output
			if len(tail) > 2000 {
				tail = tail[len(tail)-2000:]
			}
			log.Warn("project script failed", "exitCode", res.ExitCode, "timedOut", res.TimedOut, "output", string(bytes.ToValidUTF8([]byte(tail), nil)))
		default:
			log.Info("project script ran")
		}
	}
}
