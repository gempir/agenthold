package harness

import (
	acp "github.com/coder/acp-go-sdk"
)

// Default is the harness of new agents.
const Default = "claude-code"

// TerminalLoginer is implemented by adapters that know how to run a login
// method in a terminal when the harness does not say so itself.
type TerminalLoginer interface {
	TerminalLogin(methodID string) (argv []string, ok bool)
}

// AuthMethod is a login method in the form the UI shows.
type AuthMethod struct {
	ID, Name, Description string
	// Type is "terminal" (run it in the agent terminal), "agent" (the harness
	// handles it itself) or "env_var" (provide a variable, i.e. attach a secret).
	Type string
}

// DescribeAuth turns an advertised ACP auth method into what the UI shows.
func DescribeAuth(ad Adapter, m acp.AuthMethod) (AuthMethod, bool) {
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
		if tl, ok := ad.(TerminalLoginer); ok {
			if _, ok := tl.TerminalLogin(m.Agent.Id); ok {
				out.Type = "terminal"
			}
		}
		return out, true
	}
	return AuthMethod{}, false
}

// LoginCommand returns the command and environment that run a terminal login
// method: the adapter's own override, then the command the harness reports
// in "terminal-auth" metadata, then the harness command with the method's args.
func LoginCommand(ad Adapter, c Config, methods []acp.AuthMethod, methodID string) ([]string, map[string]string, bool) {
	env := map[string]string{}
	for k, v := range ad.Env(c) {
		env[k] = v
	}
	if tl, ok := ad.(TerminalLoginer); ok {
		if argv, ok := tl.TerminalLogin(methodID); ok {
			return argv, env, true
		}
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
		return append(ad.Command(c), t.Args...), env, true
	}
	return nil, nil, false
}

// AutoAuthenticator lists login methods the client may call on its own when
// the harness asks for authentication (methods backed by environment
// variables, such as API keys provided as secrets).
type AutoAuthenticator interface {
	AutoAuthenticate() []string
}

// SkillsDirer is implemented by harnesses that read Agent Skills
// (<dir>/<name>/SKILL.md). The directory is relative to the agent's home.
type SkillsDirer interface {
	SkillsDir() string
}
