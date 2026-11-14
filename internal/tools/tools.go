// Package tools turns an agent's MCP server definitions and skills into what
// its harness sees: ACP mcpServers for each session, and SKILL.md files in the
// directory the harness reads skills from.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

var secretRef = regexp.MustCompile(`\{\{\s*secret:([^}]+?)\s*\}\}`)

// SecretRefs returns the secret names referenced as {{secret:NAME}} in s.
func SecretRefs(s string) []string {
	var out []string
	for _, m := range secretRef.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// Server is a decoded MCP server definition.
type Server struct {
	db.McpServer
	ArgList   []string
	EnvMap    map[string]string
	HeaderMap map[string]string
}

// Decode parses the JSON columns of a stored server.
func Decode(s db.McpServer) Server {
	out := Server{McpServer: s, EnvMap: map[string]string{}, HeaderMap: map[string]string{}}
	_ = json.Unmarshal([]byte(s.Args), &out.ArgList)
	_ = json.Unmarshal([]byte(s.Env), &out.EnvMap)
	_ = json.Unmarshal([]byte(s.Headers), &out.HeaderMap)
	return out
}

// Refs lists every secret name the server references.
func (s Server) Refs() []string {
	var out []string
	for _, v := range s.EnvMap {
		out = append(out, SecretRefs(v)...)
	}
	for _, v := range s.HeaderMap {
		out = append(out, SecretRefs(v)...)
	}
	for _, v := range s.ArgList {
		out = append(out, SecretRefs(v)...)
	}
	out = append(out, SecretRefs(s.Url)...)
	return out
}

// ErrUnknownSecret is returned for references to secrets that do not exist.
var ErrUnknownSecret = errors.New("unknown secret")

// Secrets resolves the secrets an agent's MCP servers reference.
func Secrets(ctx context.Context, st *store.Store, workspaceID string, servers []Server) (map[string]db.Secret, error) {
	out := map[string]db.Secret{}
	for _, s := range servers {
		for _, name := range s.Refs() {
			if _, ok := out[name]; ok {
				continue
			}
			sec, err := st.GetSecretByName(ctx, db.GetSecretByNameParams{WorkspaceID: workspaceID, Name: name})
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w %q", ErrUnknownSecret, name)
			}
			if err != nil {
				return nil, err
			}
			out[name] = sec
		}
	}
	return out, nil
}

// substitute replaces {{secret:NAME}} with the secret's placeholder: the real
// value never enters the sandbox; the egress proxy swaps it on the wire.
func substitute(v string, secrets map[string]db.Secret) string {
	return secretRef.ReplaceAllStringFunc(v, func(m string) string {
		name := strings.TrimSpace(secretRef.FindStringSubmatch(m)[1])
		if s, ok := secrets[name]; ok {
			return s.Placeholder
		}
		return m
	})
}

// AgentServers loads an agent's MCP servers (own and attached).
func AgentServers(ctx context.Context, st *store.Store, agentID string) ([]Server, error) {
	rows, err := st.ListAgentMCPServers(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]Server, len(rows))
	for i, r := range rows {
		out[i] = Decode(r)
	}
	return out, nil
}

// ACPServers builds the mcpServers for an agent's sessions. Servers whose
// transport the harness does not support are left out.
func ACPServers(ctx context.Context, st *store.Store, agent db.Agent, caps acp.McpCapabilities) ([]acp.McpServer, error) {
	servers, err := AgentServers(ctx, st, agent.ID)
	if err != nil {
		return nil, err
	}
	secrets, err := Secrets(ctx, st, agent.WorkspaceID, servers)
	if err != nil {
		return nil, err
	}
	out := []acp.McpServer{}
	for _, s := range servers {
		switch s.Transport {
		case "stdio":
			env := []acp.EnvVariable{}
			for _, k := range sortedKeys(s.EnvMap) {
				env = append(env, acp.EnvVariable{Name: k, Value: substitute(s.EnvMap[k], secrets)})
			}
			args := make([]string, len(s.ArgList))
			for i, a := range s.ArgList {
				args[i] = substitute(a, secrets)
			}
			out = append(out, acp.McpServer{Stdio: &acp.McpServerStdio{Name: s.Name, Command: s.Command, Args: args, Env: env}})
		case "http":
			if !caps.Http {
				continue
			}
			headers := []acp.HttpHeader{}
			for _, k := range sortedKeys(s.HeaderMap) {
				headers = append(headers, acp.HttpHeader{Name: k, Value: substitute(s.HeaderMap[k], secrets)})
			}
			out = append(out, acp.McpServer{Http: &acp.McpServerHttpInline{Name: s.Name, Type: "http", Url: substitute(s.Url, secrets), Headers: headers}})
		}
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
