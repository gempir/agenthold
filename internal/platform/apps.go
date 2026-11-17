package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

const keepAlive = "Background processes you start from a turn (with & or nohup) are stopped when your harness ends, " +
	"so a dev server started that way dies. "

// serviceTools act on services of the calling agent's own machine.
var serviceTools = []map[string]any{
	{
		"name": "start_service",
		"description": keepAlive + "A service is how to keep something running: agenthold supervises it in your machine " +
			"(restarts it when it exits, keeps its logs) independent of your turns, until you stop it or the machine sleeps; " +
			"it starts again when the machine boots. start_service creates or updates the service and starts it " +
			"(restarts it if it runs). The command runs with sh -lc as your user in workdir. Pair it with expose_port to " +
			"get a URL for a dev server. Use service_logs to see its output.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"name", "command"},
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "Lowercase letters, digits and hyphens, up to 40."},
				"command": map[string]any{"type": "string", "description": "Shell command, for example \"npm run dev -- --port 5173\"."},
				"workdir": map[string]any{"type": "string", "description": "Directory relative to your home (default: home)."},
				"env":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			},
		},
	},
	{
		"name":        "stop_service",
		"description": "Stop a service of your machine (and everything it started). It stays defined; start_service starts it again.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"name"},
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
		},
	},
	{
		"name":        "list_services",
		"description": "List the services of your machine with their state (running, backoff, stopped, failed), restarts and last exit.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "service_logs",
		"description": "Show the last lines of a service's output (stdout and stderr). Lines starting with \"agenthold:\" come from the supervisor.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"name"},
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"lines": map[string]any{"type": "integer", "description": "Default 100, at most 1000."},
			},
		},
	},
}

// appTools manage apps: machines without a harness that run one service.
var appTools = []map[string]any{
	{
		"name": "deploy_app",
		"description": "Deploy a web app from a directory of your machine to its own always-on app machine and return its URL. " +
			"Creates the app named name if it does not exist (you can only update apps you created), copies source_dir " +
			"into a new release, runs command there with PORT set (your server must listen on that port, on 127.0.0.1 or " +
			"0.0.0.0) and waits until the port accepts connections. If it does not come up, you get its last log lines and " +
			"the previous release keeps serving. The app is reachable at the returned URL behind agenthold's sign-in " +
			"(access \"private\": your owner and the people the app is shared with; \"authenticated\": anyone signed in). " +
			"Requests carry X-Agenthold-User, X-Agenthold-Email and a signed X-Agenthold-Identity JWT. " +
			"Install dependencies before deploying (node_modules is copied) or in the command. Unlike a service on your own " +
			"machine, an app keeps running when you sleep.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"name", "source_dir", "command", "port"},
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "App name: lowercase letters, digits and hyphens, up to 40. Also its URL's name."},
				"source_dir": map[string]any{"type": "string", "description": "Directory in your machine (relative to your home or absolute). .git is left out."},
				"command":    map[string]any{"type": "string", "description": "Shell command run in the release directory, e.g. \"python3 -m http.server $PORT\" or \"node server.js\"."},
				"port":       map[string]any{"type": "integer", "description": "Port the command listens on; set as PORT."},
				"env":        map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Extra environment (plain values; secrets are attached by your owner)."},
				"access":     map[string]any{"type": "string", "enum": []string{"private", "authenticated"}},
				"memory_mb":  map[string]any{"type": "integer", "description": "Memory of a new app (default 1024)."},
				"cpus":       map[string]any{"type": "number", "description": "vCPUs of a new app (default 1)."},
			},
		},
	},
	{
		"name":        "list_apps",
		"description": "List your owner's apps with their state and URL, and whether you created them.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "app_logs",
		"description": "Show the last lines of an app's output.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"name"},
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"lines": map[string]any{"type": "integer", "description": "Default 100, at most 1000."},
			},
		},
	},
	{
		"name": "app_exec",
		"description": "Run a shell command in an app's machine (in its current release directory) and return its output " +
			"and exit code, for example a database migration or a quick check with curl localhost:$PORT. Output is cut at 64 KiB.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"name", "command"},
			"properties": map[string]any{
				"name":    map[string]any{"type": "string"},
				"command": map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer", "description": "Seconds (default 60, at most 600)."},
			},
		},
	},
	{
		"name":        "restart_app",
		"description": "Restart an app's service (starting the app machine if it is stopped).",
		"inputSchema": map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}},
	},
	{
		"name":        "stop_app",
		"description": "Stop an app's machine. Its URL stops working until it is started again.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}},
	},
	{
		"name":        "delete_app",
		"description": "Delete an app you created, with its machine and URL.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}},
	},
}

// toolArgs are the arguments of every platform tool.
type toolArgs struct {
	Agent     string            `json:"agent"`
	Text      string            `json:"text"`
	Port      int               `json:"port"`
	Name      string            `json:"name"`
	Access    string            `json:"access"`
	Command   string            `json:"command"`
	Workdir   string            `json:"workdir"`
	Env       map[string]string `json:"env"`
	Lines     int               `json:"lines"`
	SourceDir string            `json:"source_dir"`
	MemoryMB  int64             `json:"memory_mb"`
	CPUs      float64           `json:"cpus"`
	Timeout   int               `json:"timeout"`
}

func (p *Service) agentAudit(ctx context.Context, caller db.Agent, machineID, action, target string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["fromAgent"], details["fromAgentId"] = caller.Name, caller.ID
	p.Audit.Record(ctx, audit.Entry{AgentID: machineID, UserID: caller.OwnerID, Action: action, Target: target, Details: details})
}

func describeStates(sts []tunnel.ServiceState, name string) string {
	for _, st := range sts {
		if st.Name == name {
			return describeState(st)
		}
	}
	return "defined (the machine starts it when it boots)"
}

func describeState(st tunnel.ServiceState) string {
	out := st.State
	if st.Pid != 0 {
		out += fmt.Sprintf(", pid %d", st.Pid)
	}
	if st.Restarts > 0 {
		out += fmt.Sprintf(", %d restarts", st.Restarts)
	}
	switch {
	case st.Signal != "":
		out += ", last ended by signal " + st.Signal
	case st.ExitCode != nil && st.State != tunnel.ServiceRunning:
		out += fmt.Sprintf(", last exit code %d", *st.ExitCode)
	}
	if st.Error != "" {
		out += ": " + st.Error
	}
	return out
}

func (p *Service) startService(ctx context.Context, caller db.Agent, in toolArgs) (string, error) {
	spec := apps.Spec{Name: in.Name, Command: in.Command, Workdir: in.Workdir, Env: in.Env, Enabled: true}
	changed, sts, err := p.Apps.Put(ctx, caller.ID, spec)
	if err != nil {
		return "", err
	}
	if !changed {
		if sts, err = p.Apps.Restart(ctx, caller.ID, in.Name); err != nil {
			return "", err
		}
	}
	p.agentAudit(ctx, caller, caller.ID, audit.ServicePut, in.Name, map[string]any{"command": in.Command, "workdir": in.Workdir})
	return fmt.Sprintf("Service %s: %s. It keeps running across your turns; see service_logs for its output.", in.Name, describeStates(sts, in.Name)), nil
}

func (p *Service) stopService(ctx context.Context, caller db.Agent, name string) (string, error) {
	if _, err := p.Apps.SetEnabled(ctx, caller.ID, name, false); err != nil {
		return "", err
	}
	p.agentAudit(ctx, caller, caller.ID, audit.ServiceStop, name, nil)
	return fmt.Sprintf("Service %s is stopped.", name), nil
}

type serviceInfo struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
}

func (p *Service) listServices(ctx context.Context, caller db.Agent) (string, error) {
	rows, err := p.Apps.List(ctx, caller.ID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "No services are defined. Use start_service to keep a process running.", nil
	}
	live, _, err := p.Apps.Status(ctx, caller.ID)
	if err != nil {
		return "", err
	}
	out := make([]serviceInfo, 0, len(rows))
	for _, r := range rows {
		s := apps.SpecOf(r)
		info := serviceInfo{Name: s.Name, Command: s.Command, Workdir: s.Workdir, Enabled: s.Enabled, Status: "not running"}
		if st, ok := live[s.Name]; ok {
			info.Status = describeState(st)
		}
		out = append(out, info)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

func logLines(n int) int {
	if n <= 0 {
		return 100
	}
	return min(n, 1000)
}

func (p *Service) serviceLogs(ctx context.Context, machineID, name string, lines int) (string, error) {
	var buf bytes.Buffer
	if err := p.Apps.Logs(ctx, machineID, name, logLines(lines), false, &buf); err != nil {
		return "", err
	}
	if buf.Len() == 0 {
		return "(no output yet)", nil
	}
	// The reply is one tunnel frame back to the guest: keep the end.
	if b := buf.Bytes(); len(b) > maxToolLog {
		b = b[len(b)-maxToolLog:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
		return "(earlier output cut)\n" + string(b), nil
	}
	return buf.String(), nil
}

// maxToolLog bounds the log text a tool returns.
const maxToolLog = 128 << 10

var appNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ownerApps lists the apps of the caller's owner.
func (p *Service) ownerApps(ctx context.Context, caller db.Agent) ([]db.ListUserAgentsRow, error) {
	rows, err := p.Store.ListUserAgents(ctx, caller.OwnerID)
	if err != nil {
		return nil, err
	}
	var out []db.ListUserAgentsRow
	for _, r := range rows {
		if r.Agent.Kind == apps.KindApp && r.Agent.OwnerID == caller.OwnerID {
			out = append(out, r)
		}
	}
	return out, nil
}

// ErrNoApp is returned for an app name that does not exist (or is not the owner's).
var ErrNoApp = errors.New("no app with that name; see list_apps")

// findApp resolves an app of the caller's owner by name or id. mine requires
// that the caller created it.
func (p *Service) findApp(ctx context.Context, caller db.Agent, ref string, mine bool) (db.Agent, error) {
	rows, err := p.ownerApps(ctx, caller)
	if err != nil {
		return db.Agent{}, err
	}
	ref = strings.TrimSpace(ref)
	for _, r := range rows {
		if r.Agent.ID == ref || r.Agent.Name == ref {
			if mine && r.Agent.CreatedByAgentID != caller.ID {
				return db.Agent{}, fmt.Errorf("app %s was not created by you, so you cannot change it", r.Agent.Name)
			}
			return r.Agent, nil
		}
	}
	return db.Agent{}, ErrNoApp
}

func (p *Service) deployApp(ctx context.Context, caller db.Agent, in toolArgs) (string, error) {
	if !appNameRE.MatchString(in.Name) {
		return "", errors.New("name must be 1 to 40 lowercase letters, digits and hyphens, not starting with a hyphen")
	}
	access := preview.Access(in.Access)
	if access == preview.Public {
		return "", ErrPublic
	}
	if access != "" && access != preview.Private && access != preview.Authenticated {
		return "", errors.New("access must be private or authenticated")
	}
	lim := p.Apps.Limits
	cpus, mem := lim.DefaultCPUs, lim.DefaultMemoryMB
	if in.CPUs != 0 {
		cpus = in.CPUs
	}
	if in.MemoryMB != 0 {
		mem = in.MemoryMB
	}
	if cpus < 1 || cpus > lim.MaxCPUs {
		return "", fmt.Errorf("cpus must be between 1 and %g", lim.MaxCPUs)
	}
	if mem < config.MinMemoryMB || mem > lim.MaxMemoryMB {
		return "", fmt.Errorf("memory_mb must be between %d and %d", config.MinMemoryMB, lim.MaxMemoryMB)
	}
	app, err := p.findApp(ctx, caller, in.Name, true)
	created := false
	switch {
	case errors.Is(err, ErrNoApp):
		mine, err := p.Store.ListAgentApps(ctx, caller.ID)
		if err != nil {
			return "", err
		}
		if len(mine) >= lim.MaxPerAgent {
			return "", fmt.Errorf("you already created %d apps, the most an agent may have; delete one with delete_app or update an existing one", len(mine))
		}
		if app, err = p.Apps.Create(ctx, apps.CreateParams{
			OwnerID: caller.OwnerID, Name: in.Name, CreatedByAgentID: caller.ID, CPUs: cpus, MemoryMB: mem,
		}); err != nil {
			return "", err
		}
		created = true
		p.agentAudit(ctx, caller, app.ID, audit.AppCreate, app.Name, map[string]any{"cpus": cpus, "memoryMb": mem})
	case err != nil:
		return "", err
	}
	res, err := p.Apps.Deploy(ctx, apps.DeployParams{
		App: app, SourceID: caller.ID, SourceDir: in.SourceDir, Command: in.Command, Port: in.Port, Env: in.Env,
		Expose: true, Access: access, ExposedBy: "agent:" + caller.ID, ActorUserID: caller.OwnerID, ActorAgentID: caller.ID,
	})
	if err != nil {
		return "", fmt.Errorf("deploy %s: %w", app.Name, err)
	}
	var b strings.Builder
	verb := "Updated"
	if created {
		verb = "Created"
	}
	fmt.Fprintf(&b, "%s app %s (release %d).", verb, app.Name, res.Release)
	switch {
	case res.Ready && res.URL != "":
		fmt.Fprintf(&b, " It is serving at %s (access: %s).", res.URL, res.Exposure.Access)
	case res.Ready:
		b.WriteString(" It is running, but this server has no preview URLs configured, so it has no URL.")
	case res.RolledBack:
		fmt.Fprintf(&b, " The new release did not accept connections on port %d within 60 seconds, so the previous release serves again.", in.Port)
	default:
		fmt.Fprintf(&b, " It did not accept connections on port %d within 60 seconds.", in.Port)
	}
	if !res.Ready {
		if res.Service != nil {
			fmt.Fprintf(&b, " Service state: %s.", describeState(*res.Service))
		}
		fmt.Fprintf(&b, " Check that it listens on $PORT (%d). Last log lines:\n%s", in.Port, res.Logs)
	}
	if !created && (in.CPUs != 0 && in.CPUs != app.Cpus || in.MemoryMB != 0 && in.MemoryMB != app.MemoryMb) {
		b.WriteString("\nNote: cpus and memory_mb only apply to a new app; your owner can resize this one.")
	}
	return b.String(), nil
}

type appInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	CreatedBy string `json:"createdBy"`
}

func (p *Service) listApps(ctx context.Context, caller db.Agent) (string, error) {
	rows, err := p.ownerApps(ctx, caller)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "There are no apps. deploy_app creates one.", nil
	}
	out := make([]appInfo, 0, len(rows))
	for _, r := range rows {
		info := appInfo{ID: r.Agent.ID, Name: r.Agent.Name, State: r.Sandbox.State, CreatedBy: "your owner"}
		switch r.Agent.CreatedByAgentID {
		case caller.ID:
			info.CreatedBy = "you"
		case "":
		default:
			info.CreatedBy = "another agent"
		}
		if p.Preview.Enabled() {
			if es, err := p.Preview.List(ctx, r.Agent.ID); err == nil && len(es) > 0 {
				info.URL = p.Preview.URL(es[0])
			}
		}
		out = append(out, info)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

func (p *Service) appExec(ctx context.Context, caller db.Agent, in toolArgs) (string, error) {
	if strings.TrimSpace(in.Command) == "" {
		return "", errors.New("command is required")
	}
	app, err := p.findApp(ctx, caller, in.Name, false)
	if err != nil {
		return "", err
	}
	r, err := p.Apps.Exec(ctx, app.ID, in.Command, in.Timeout)
	if err != nil {
		return "", err
	}
	p.agentAudit(ctx, caller, app.ID, audit.AppExec, "", map[string]any{"command": truncate(in.Command, 500), "exitCode": r.ExitCode})
	var b strings.Builder
	fmt.Fprintf(&b, "Exit code %d.", r.ExitCode)
	if r.TimedOut {
		b.WriteString(" (timed out and killed)")
	}
	if r.Truncated {
		b.WriteString(" (output cut at 64 KiB)")
	}
	b.WriteString("\n")
	b.WriteString(r.Output)
	return b.String(), nil
}

func (p *Service) restartApp(ctx context.Context, caller db.Agent, name string) (string, error) {
	app, err := p.findApp(ctx, caller, name, false)
	if err != nil {
		return "", err
	}
	if p.Apps.Wake == nil {
		return "", errors.New("not available")
	}
	if _, err := p.Apps.Wake(ctx, app.ID, func(string) {}); err != nil {
		return "", err
	}
	sts, err := p.Apps.Restart(ctx, app.ID, apps.AppService)
	if errors.Is(err, apps.ErrNotFound) {
		return "", fmt.Errorf("app %s has nothing deployed yet; use deploy_app", app.Name)
	}
	if err != nil {
		return "", err
	}
	p.agentAudit(ctx, caller, app.ID, audit.ServiceRestart, apps.AppService, nil)
	return fmt.Sprintf("App %s restarted: %s.", app.Name, describeStates(sts, apps.AppService)), nil
}

func (p *Service) stopApp(ctx context.Context, caller db.Agent, name string) (string, error) {
	app, err := p.findApp(ctx, caller, name, false)
	if err != nil {
		return "", err
	}
	if err := p.Apps.Sandboxes.Stop(ctx, app.ID); err != nil {
		return "", err
	}
	p.agentAudit(ctx, caller, app.ID, audit.AppStop, "", nil)
	return fmt.Sprintf("App %s is stopping. restart_app or deploy_app starts it again.", app.Name), nil
}

func (p *Service) deleteApp(ctx context.Context, caller db.Agent, name string) (string, error) {
	app, err := p.findApp(ctx, caller, name, true)
	if err != nil {
		return "", err
	}
	if err := p.Apps.Sandboxes.Destroy(ctx, app.ID); err != nil {
		return "", err
	}
	if err := p.Preview.DeleteAgent(ctx, app); err != nil {
		return "", err
	}
	p.agentAudit(ctx, caller, app.ID, audit.AppDelete, app.Name, nil)
	return fmt.Sprintf("App %s is being deleted.", app.Name), nil
}
