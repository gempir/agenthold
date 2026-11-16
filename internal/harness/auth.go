package harness

import (
	"slices"

	acp "github.com/coder/acp-go-sdk"
)

// Default is the harness of new agents.
const Default = "claude-code"

// AuthMethod is a login method in the form the UI shows.
type AuthMethod struct {
	ID, Name, Description string
	// Type is "terminal" (run it in the agent terminal), "agent" (the harness
	// handles it itself) or "env_var" (provide a variable, i.e. attach a secret).
	Type string
}

// DescribeAuth turns an advertised ACP auth method into what the UI shows.
func DescribeAuth(ad *Adapter, m acp.AuthMethod) (AuthMethod, bool) {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch {
	case m.Terminal != nil:
		return AuthMethod{ID: m.Terminal.Id, Name: m.Terminal.Name, Description: str(m.Terminal.Description), Type: "terminal"}, true
	case m.EnvVar != nil:
		return AuthMethod{ID: m.EnvVar.Id, Name: m.EnvVar.Name, Description: "Provide it as an environment variable: attach a secret", Type: "env_var"}, true
	case m.Agent != nil:
		out := AuthMethod{ID: m.Agent.Id, Name: m.Agent.Name, Description: str(m.Agent.Description), Type: "agent"}
		if _, ok := ad.TerminalLogins[m.Agent.Id]; ok {
			out.Type = "terminal"
		}
		return out, true
	}
	return AuthMethod{}, false
}

// LoginCommand returns the command and environment that run a terminal login
// method: the adapter's own override, then the command the harness reports
// in "terminal-auth" metadata, then the harness command with the method's args.
func LoginCommand(ad *Adapter, c Config, methods []acp.AuthMethod, methodID string) ([]string, map[string]string, bool) {
	env := ad.Environment(c)
	if argv, ok := ad.TerminalLogins[methodID]; ok {
		return slices.Clone(argv), env, true
	}
	for _, m := range methods {
		t := m.Terminal
		if t == nil || t.Id != methodID {
			continue
		}
		for k, v := range t.Env {
			if s, ok := v.(string); ok {
				env[k] = s
			}
		}
		if ta, ok := t.Meta["terminal-auth"].(map[string]any); ok {
			if cmd, ok := ta["command"].(string); ok && cmd != "" {
				argv := []string{cmd}
				if args, ok := ta["args"].([]any); ok {
					for _, x := range args {
						if s, ok := x.(string); ok {
							argv = append(argv, s)
						}
					}
				}
				return argv, env, true
			}
		}
		return append(slices.Clone(ad.Command), t.Args...), env, true
	}
	return nil, nil, false
}
