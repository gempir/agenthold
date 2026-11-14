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

// Exit is sent on a pty stream by sandboxd when the process ends.
type Exit struct {
	Type string `json:"type"` // "exit"
	Code int    `json:"code"`
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
