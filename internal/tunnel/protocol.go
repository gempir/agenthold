package tunnel

import (
	"encoding/json"
	"os"
)

// Operations a stream can open.
const (
	OpPTY      = "pty"      // interactive terminal
	OpFSList   = "fs.list"  // list a directory
	OpFSRead   = "fs.read"  // download a file
	OpFSWrite  = "fs.write" // upload a file
	OpFSMkdir  = "fs.mkdir"
	OpFSRemove = "fs.remove"
	OpPing     = "ping"
	OpStats    = "stats"    // resource usage, for idle detection
	OpHarness  = "harness"  // agent harness process speaking ACP on stdio
	OpPlatform = "platform" // opened by sandboxd: a platform MCP tool call
	OpHello    = "hello"    // the server asks sandboxd who it is right after connecting
	OpShutdown = "shutdown" // power the sandbox off (microVM init mode); answered with OK first
	// OpDial connects to a TCP port on the guest's loopback. sandboxd answers
	// with a DialReply; after a successful one the stream carries raw bytes
	// in both directions (no frames) until either side closes it.
	OpDial = "dial"
	// OpService manages the services sandboxd supervises (see ServiceRequest).
	// Every action but logs answers with a ServicesReply; logs answers with
	// OK, then data frames of log output, then (without Follow) an EOF frame.
	OpService = "service"
	// OpFSTar streams a directory out of the sandbox as a tar archive (data
	// frames, then EOF), read as the agent account. OpFSUntar extracts the tar
	// archive in the data frames that follow into a directory, as the agent
	// account, and answers OK after the EOF frame.
	OpFSTar   = "fs.tar"
	OpFSUntar = "fs.untar"
	// OpFSSymlink points the symlink Path at Target, replacing what is there
	// atomically.
	OpFSSymlink = "fs.symlink"
	// OpExec runs Cmd as the agent account and answers with an ExecReply.
	OpExec = "exec"
)

// Open is the first frame on every stream.
type Open struct {
	Op string `json:"op"`

	// pty
	Cmd  []string          `json:"cmd,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	Cols uint16            `json:"cols,omitempty"`
	Rows uint16            `json:"rows,omitempty"`

	// fs.*
	Path string      `json:"path,omitempty"`
	Mode os.FileMode `json:"mode,omitempty"`
	// Recursive applies to fs.remove and fs.mkdir.
	Recursive bool `json:"recursive,omitempty"`
	// Deny lists absolute paths (files or directories) that fs operations must
	// refuse, checked after resolving symlinks.
	Deny []string `json:"deny,omitempty"`

	// dial
	Port int `json:"port,omitempty"`

	// service
	Service *ServiceRequest `json:"service,omitempty"`

	// fs.tar: paths (relative to Path) left out of the archive; MaxBytes
	// bounds the archive (fs.tar and fs.untar).
	Exclude  []string `json:"exclude,omitempty"`
	MaxBytes int64    `json:"maxBytes,omitempty"`
	// fs.symlink
	Target string `json:"target,omitempty"`
	// exec: the working directory (default the home) and the time limit in
	// seconds (default 60); MaxBytes bounds the output kept.
	Dir     string `json:"dir,omitempty"`
	Timeout int    `json:"timeout,omitempty"`

	// platform (opened by sandboxd): a platform tool call from an MCP client.
	Tool    string          `json:"tool,omitempty"`
	Session string          `json:"session,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
}

// OK is the generic success reply.
type OK struct {
	OK bool `json:"ok"`
}

// Resize is sent on a pty stream by the server.
type Resize struct {
	Type string `json:"type"` // "resize"
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Exit is sent on a pty or harness stream by sandboxd when the process
// ends. A harness killed by a signal exits with 128+signal.
type Exit struct {
	Type string `json:"type"` // "exit"
	Code int    `json:"code"`
	// Stopped: sandboxd killed the harness itself (the server went away, or
	// the harness closed its stdout), so a signal is not an outside kill.
	Stopped bool `json:"stopped,omitempty"`
}

// Stats answers OpStats.
type Stats struct {
	// CPUTicks is the CPU time (in USER_HZ ticks, 1/100 s) used so far by all
	// processes in the sandbox except sandboxd itself.
	CPUTicks uint64 `json:"cpuTicks"`
	// Processes is the number of processes, excluding sandboxd.
	Processes int `json:"processes"`
}

// Stderr carries one line of a harness's stderr.
type Stderr struct {
	Type string `json:"type"` // "stderr"
	Line string `json:"line"`
}

// FileInfo describes one directory entry.
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	IsDir   bool   `json:"isDir"`
	IsLink  bool   `json:"isLink"`
	ModTime int64  `json:"modTime"`
}

// ListReply answers fs.list.
type ListReply struct {
	Path    string     `json:"path"`
	Entries []FileInfo `json:"entries"`
}

// ReadReply precedes the data frames of fs.read.
type ReadReply struct {
	Size int64 `json:"size"`
}

// Hello is the first message sandboxd sends on the control stream.
type Hello struct {
	Version string `json:"version"`
	User    string `json:"user"`
	Home    string `json:"home"`
}

// DialReply answers OpDial. Refused is set when nothing listens on the port.
type DialReply struct {
	OK      bool   `json:"ok"`
	Refused bool   `json:"refused,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Service actions.
const (
	ServiceApply   = "apply"   // replace the whole desired set with Specs
	ServiceStart   = "start"   // start Name (enable it)
	ServiceStop    = "stop"    // stop Name (disable it)
	ServiceRestart = "restart" // restart Name now, resetting its backoff
	ServiceStatus  = "status"  // report every service
	ServiceLogs    = "logs"    // the last Lines lines of Name's log, then Follow
)

// ServiceRequest is the body of an OpService stream.
type ServiceRequest struct {
	Action string        `json:"action"`
	Name   string        `json:"name,omitempty"`
	Specs  []ServiceSpec `json:"specs,omitempty"`
	Lines  int           `json:"lines,omitempty"`
	Follow bool          `json:"follow,omitempty"`
}

// ServiceSpec is a supervised command. Command is run by the login shell
// ("sh -lc") as the agent account in Workdir (relative to the home; default
// the home) with Env added to the environment.
type ServiceSpec struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Workdir string            `json:"workdir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Enabled bool              `json:"enabled"`
}

// Service states.
const (
	ServiceRunning = "running"
	ServiceBackoff = "backoff" // exited; restarting after a delay
	ServiceStopped = "stopped"
	ServiceFailed  = "failed" // could not be started (retried after a delay)
)

// ServiceState reports one supervised service.
type ServiceState struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Pid      int    `json:"pid,omitempty"`
	Restarts int    `json:"restarts"`
	// ExitCode and Signal describe the last exit (Signal is a name like
	// "killed" when a signal ended it).
	ExitCode  *int   `json:"exitCode,omitempty"`
	Signal    string `json:"signal,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"` // unix seconds of the current or last start
}

// ServicesReply answers every OpService action but logs.
type ServicesReply struct {
	Services []ServiceState `json:"services"`
}

// ExecReply answers OpExec.
type ExecReply struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	ExitCode  int    `json:"exitCode"`
	TimedOut  bool   `json:"timedOut,omitempty"`
}
