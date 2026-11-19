package sandboxd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// ResumePort is the guest vsock port sandboxd listens on for the host's
// resume message. The host connects to it right after it resumed the VM from
// a memory snapshot (Firecracker's vsock device keeps listening sockets
// across a snapshot and resets connections).
const ResumePort = 1100

// ResumeVersion is the version of ResumeMessage. The host records it with a
// snapshot and resumes only snapshots taken with the same version.
const ResumeVersion = 1

// ResumeMessage is what the host sends once after resuming the VM: the boot
// configuration of this wake (the old tunnel token is no longer valid) and
// the host's wall clock, which the guest's own clock lags by the time the
// VM was asleep. One JSON object, answered with a ResumeReply.
type ResumeMessage struct {
	Version int    `json:"version"`
	AgentID string `json:"agentId"`
	// Token is the tunnel token of this wake. Empty keeps the current one:
	// the host resumed a VM that an interrupted sleep left paused, and
	// only the clock and the connection need fixing.
	Token string `json:"token,omitempty"`
	// TimeUnixNano is the host's wall clock when it sent the message.
	TimeUnixNano int64 `json:"timeUnixNano"`
	// Files are the boot files of this wake (see GuestBoot.Files).
	Files map[string][]byte `json:"files,omitempty"`
}

// ResumeReply answers a ResumeMessage.
type ResumeReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// ClockStepMillis is how far the guest clock was stepped.
	ClockStepMillis int64 `json:"clockStepMillis,omitempty"`
}

// ResumeMaxSize bounds a resume message (it carries the boot files).
const ResumeMaxSize = GuestBootMaxSize

// readResume decodes one resume message from r.
func readResume(r io.Reader) (ResumeMessage, error) {
	var m ResumeMessage
	dec := json.NewDecoder(io.LimitReader(r, ResumeMaxSize))
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("resume message: %w", err)
	}
	if m.Version != ResumeVersion {
		return m, fmt.Errorf("resume message version %d, want %d", m.Version, ResumeVersion)
	}
	if m.AgentID == "" || m.TimeUnixNano <= 0 {
		return m, errors.New("resume message: agentId and time are required")
	}
	for p := range m.Files {
		if err := checkGuestPath(p); err != nil {
			return m, fmt.Errorf("resume message: file %q: %w", p, err)
		}
	}
	return m, nil
}

// resume applies a resume message: clock, boot files, the new token (also
// in the boot file a restarted daemon reads), then a prompt redial. setClock
// is the platform's clock step.
func (d *Daemon) resume(m ResumeMessage, setClock func(time.Time) error) ResumeReply {
	if m.AgentID != d.cfg.Boot.AgentID {
		return ResumeReply{Error: "resume message is for another machine"}
	}
	host := time.Unix(0, m.TimeUnixNano)
	step := time.Until(host)
	if err := setClock(host); err != nil {
		// Keep going: a stale clock is better than an unreachable machine.
		d.log.Warn("resume: set clock", "err", err)
	}
	services, hasServices := m.Files[d.cfg.ServicesFile]
	if d.cfg.ServicesFile != "" && hasServices {
		// Applied while the supervisor's operations wait, so no runtime
		// change can save the set it had before the sleep over this file.
		d.services.ops.Lock()
		defer d.services.ops.Unlock()
	}
	for _, p := range slices.Sorted(maps.Keys(m.Files)) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			d.log.Warn("resume: boot file", "path", p, "err", err)
			continue
		}
		if err := writeFileAtomic(p, m.Files[p], 0o644); err != nil {
			d.log.Warn("resume: boot file", "path", p, "err", err)
		}
	}
	if _, ok := m.Files[CAFile]; ok {
		d.installCA()
	}
	if d.cfg.ServicesFile != "" && hasServices {
		// The services the host delivers (changed while the machine slept).
		// The file already holds them: apply without saving.
		if specs, err := ParseServices(services); err != nil {
			d.log.Warn("resume: services", "err", err)
		} else if err := d.services.apply(specs, false); err != nil {
			d.log.Warn("resume: services", "err", err)
		}
	}
	if m.Token != "" && d.cfg.BootPath != "" {
		d.bootMu.Lock()
		b := d.boot
		d.bootMu.Unlock()
		b.Token = m.Token
		if data, err := json.Marshal(b); err == nil {
			if err := writeFileAtomic(d.cfg.BootPath, data, 0o600); err != nil {
				d.log.Warn("resume: boot file", "err", err)
			}
		}
	}
	d.setToken(m.Token)
	d.log.Info("resumed from a memory snapshot", "clockStep", step.Round(time.Millisecond))
	return ResumeReply{OK: true, ClockStepMillis: step.Milliseconds()}
}
