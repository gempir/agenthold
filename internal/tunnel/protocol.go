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
	OpExecutor = "executor" // opened by sandboxd: persistent JSON-RPC stream to owner's Executor MCP server
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
	// frames, then EOF). OpFSUntar extracts the tar archive in the data frames
	// that follow into a new directory inside the home and answers OK after
	// the EOF frame. sandboxd does both as root through an os.Root (it never
	// follows a symlink out of the directory) and gives extracted entries to
	// the agent account.
	OpFSTar   = "fs.tar"
	OpFSUntar = "fs.untar"
	// OpFSSymlink points the symlink Path (inside the home) at Target,
	// replacing what is there atomically.
	OpFSSymlink = "fs.symlink"
	// OpExec runs Cmd as the agent account and answers with an ExecReply.
	OpExec = "exec"
	// OpSuspend tells sandboxd that the VM is about to be paused and snapshot
	// (sleep with memory kept): it flushes the guest's filesystems and answers
	// OK. A sandboxd that answers with an error (older ones do not know the
	// op) does not support resuming from a snapshot.
	OpSuspend = "suspend"
	// OpGit runs a git action (see GitRequest) as the agent account and
	// answers with a GitReply.
	OpGit = "git"
	// OpFSReadText reads a file inside the home for a viewer or editor and
	// answers with a TextFile as data-framed JSON (see WriteJSONData).
	OpFSReadText = "fs.read_text"
	// OpFSWriteText replaces a file inside the home with the data frames
	// that follow (atomically, keeping an existing file's mode) and answers
	// with a TextFile (no content) after the EOF frame.
	OpFSWriteText = "fs.write_text"
	// OpFSSearch searches file paths below Path (inside the home) for Query
	// and answers with a FileSearchReply.
	OpFSSearch = "fs.search"
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

	// git
	Git *GitRequest `json:"git,omitempty"`

	// fs.read_text: an optional 1-based inclusive line range (0: unbounded).
	// fs.search: Query and Limit.
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	Query     string `json:"query,omitempty"`
	Limit     int    `json:"limit,omitempty"`

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

	// The rest is live usage of the whole machine for the admin overview. A
	// guest that cannot report a value (an older sandboxd, a platform without
	// procfs) leaves it zero.
	//
	// CPUBusyTicks and CPUTotalTicks are the machine's cumulative CPU time in
	// USER_HZ ticks, all processes and services included: busy is everything
	// but idle and iowait, total is the sum over all vCPUs. The usage between
	// two readings is the busy delta over the total delta.
	CPUBusyTicks  uint64 `json:"cpuBusyTicks,omitempty"`
	CPUTotalTicks uint64 `json:"cpuTotalTicks,omitempty"`
	// MemTotalBytes is the guest's RAM, MemUsedBytes what is not available
	// (total minus the kernel's MemAvailable).
	MemTotalBytes uint64 `json:"memTotalBytes,omitempty"`
	MemUsedBytes  uint64 `json:"memUsedBytes,omitempty"`
	// HomeTotalBytes and HomeUsedBytes describe the persistent home volume,
	// RootTotalBytes and RootUsedBytes the system (overlay) volume.
	HomeTotalBytes uint64 `json:"homeTotalBytes,omitempty"`
	HomeUsedBytes  uint64 `json:"homeUsedBytes,omitempty"`
	RootTotalBytes uint64 `json:"rootTotalBytes,omitempty"`
	RootUsedBytes  uint64 `json:"rootUsedBytes,omitempty"`
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
	// Target is where a symlink points.
	Target string `json:"target,omitempty"`
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

// Git actions.
const (
	GitList        = "list"         // repositories in the home
	GitStatus      = "status"       // the repository Path is in (none: Repo is nil)
	GitClone       = "clone"        // clone URL into Path (default projects/<name>)
	GitCheckout    = "checkout"     // switch Path to Branch (Create: new from FromRef)
	GitWorktreeAdd = "worktree_add" // add a worktree of Path for Branch at WorktreePath
	GitPull        = "pull"         // fast-forward pull (FetchOnly: fetch --prune)
	GitConfig      = "config"       // write the agent's commit identity
	// GitPush pushes the branch Path is on (Force: --force-with-lease),
	// setting its upstream on the first push.
	GitPush = "push"
	// GitWorktreeRemove removes the worktree at Path (Force: even when it
	// has changes); the reply describes its main repository.
	GitWorktreeRemove = "worktree_remove"
	// GitInit makes the directory Path (created when missing) a repository
	// on Branch (default main) and commits what it holds; a directory that
	// is a repository's root already is left alone.
	GitInit = "init"
	// GitChanges reports what the branch Path is on changed since it forked
	// from FromRef: commits, and files changed in them or uncommitted.
	GitChanges = "changes"
	// GitMerge merges Branch into the branch the repository Path is on
	// (always a merge commit; Message). It needs a clean work tree; on a
	// conflict it aborts and fails, naming the conflicting files.
	GitMerge = "merge"
	// GitBranchDelete deletes the branch Branch of the repository Path
	// (Force: also when it is not merged).
	GitBranchDelete = "branch_delete"
	// GitDiff reports a structured diff of the repository Path is in (see
	// DiffMode); the reply (Diff) is data-framed JSON (see WriteJSONData).
	GitDiff = "diff"
	// GitLog reports the last Limit commits of HEAD (Commits), as
	// data-framed JSON.
	GitLog = "log"
	// GitCreateCommit commits what is staged (All: everything, git add -A
	// first) with Message; the reply carries the repository and the new
	// Commit.
	GitCreateCommit = "commit"
)

// Diff modes of GitDiff.
const (
	DiffUncommitted = "uncommitted" // staged, unstaged and untracked changes against HEAD
	DiffBranch      = "branch"      // the work tree against the merge base with Base (default: the default branch)
	DiffCommit      = "commit"      // the commit Ref against its first parent
)

// GitRequest is the body of an OpGit stream. Paths are relative to the home
// and must stay inside it.
type GitRequest struct {
	Action       string `json:"action"`
	Path         string `json:"path,omitempty"`
	URL          string `json:"url,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Create       bool   `json:"create,omitempty"`
	FromRef      string `json:"fromRef,omitempty"`
	WorktreePath string `json:"worktreePath,omitempty"`
	FetchOnly    bool   `json:"fetchOnly,omitempty"`
	Force        bool   `json:"force,omitempty"`
	AuthorName   string `json:"authorName,omitempty"`
	AuthorEmail  string `json:"authorEmail,omitempty"`
	Message      string `json:"message,omitempty"`
	// diff and log
	Mode    string `json:"mode,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Base    string `json:"base,omitempty"`
	Context *int   `json:"context,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	// commit: stage everything first
	All bool `json:"all,omitempty"`
}

// GitRepo describes one repository (or worktree). Path and MainPath are
// relative to the home; RemoteURL never carries credentials.
type GitRepo struct {
	Path       string `json:"path"`
	Project    string `json:"project"`
	RemoteURL  string `json:"remoteUrl,omitempty"`
	Branch     string `json:"branch"`
	Detached   bool   `json:"detached,omitempty"`
	Dirty      bool   `json:"dirty,omitempty"`
	Ahead      int    `json:"ahead,omitempty"`
	Behind     int    `json:"behind,omitempty"`
	IsWorktree bool   `json:"isWorktree,omitempty"`
	MainPath   string `json:"mainPath,omitempty"`
}

// GitReply answers OpGit.
type GitReply struct {
	Home     string        `json:"home,omitempty"`
	Repos    []GitRepo     `json:"repos,omitempty"`
	Repo     *GitRepo      `json:"repo,omitempty"`
	Branches []string      `json:"branches,omitempty"`
	Output   string        `json:"output,omitempty"`
	Changes  *GitChangeSet `json:"changes,omitempty"`
	Diff     *GitDiffSet   `json:"diff,omitempty"`
	Commits  []GitCommit   `json:"commits,omitempty"`
	Commit   *GitCommit    `json:"commit,omitempty"`
}

// GitDiffSet answers GitDiff. Paths are relative to the repository root.
type GitDiffSet struct {
	Mode string `json:"mode"`
	// Base is the ref a branch diff compares with, MergeBase the commit it
	// forked from; Ref is the commit a commit diff shows.
	Base      string        `json:"base,omitempty"`
	MergeBase string        `json:"mergeBase,omitempty"`
	Ref       string        `json:"ref,omitempty"`
	Files     []GitDiffFile `json:"files"`
	Additions int           `json:"additions"`
	Deletions int           `json:"deletions"`
	// Truncated: more files changed than are listed.
	Truncated bool `json:"truncated,omitempty"`
}

// GitDiffFile is one changed file. Status is A, M, D, R or ? (untracked).
// Truncated: the patch was left out or cut because it is too large.
type GitDiffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Patch     string `json:"patch,omitempty"`
}

// GitCommit describes one commit. Date is unix seconds (author date).
type GitCommit struct {
	SHA          string `json:"sha"`
	ShortSHA     string `json:"shortSha"`
	Subject      string `json:"subject"`
	Body         string `json:"body,omitempty"`
	Author       string `json:"author"`
	Email        string `json:"email"`
	Date         int64  `json:"date"`
	FilesChanged int    `json:"filesChanged"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
}

// TextFile answers fs.read_text and fs.write_text. Path is relative to the
// home. Content is the text (empty for binary files); with a line range,
// StartLine and EndLine are the lines it holds and TotalLines is set when
// the whole file was read.
type TextFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModTime    int64  `json:"modTime"`
	MimeType   string `json:"mimeType"`
	IsBinary   bool   `json:"isBinary"`
	Truncated  bool   `json:"truncated"`
	Content    string `json:"content"`
	StartLine  int    `json:"startLine,omitempty"`
	EndLine    int    `json:"endLine,omitempty"`
	TotalLines int    `json:"totalLines,omitempty"`
}

// FileMatch is one fs.search result; Path is relative to the search base.
type FileMatch struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir,omitempty"`
}

// FileSearchReply answers fs.search. Base is relative to the home.
type FileSearchReply struct {
	Base      string      `json:"base"`
	Results   []FileMatch `json:"results"`
	Truncated bool        `json:"truncated,omitempty"`
}

// GitChangeSet answers GitChanges. Files are relative to the repository root;
// Status is git's letter (A, M, D, R, ...; "?" untracked).
type GitChangeSet struct {
	Base      string          `json:"base"`
	Commits   int             `json:"commits"`
	Dirty     bool            `json:"dirty,omitempty"`
	Files     []GitFileChange `json:"files,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

// GitFileChange is one changed file.
type GitFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}
