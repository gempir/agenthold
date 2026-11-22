package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// SkillName is the allowed form of a skill's name (its directory).
var SkillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

const manifestName = ".agenthold-managed.json"

// SkillMarkdown renders a skill as SKILL.md with YAML front matter.
func SkillMarkdown(s db.Skill) string {
	desc := strings.ReplaceAll(strings.TrimSpace(s.Description), "\n", " ")
	quoted, _ := json.Marshal(desc) // a JSON string is a valid YAML scalar
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", s.Name, quoted, strings.TrimSpace(s.Content))
}

// InstallSkills writes the agent's skills into dir (relative to the agent's
// home) and removes skills it installed earlier that are no longer attached.
// Skills the agent created itself are left alone.
func InstallSkills(ctx context.Context, ts *tunnel.Session, dir string, skills []db.Skill) error {
	var previous []string
	var buf strings.Builder
	manifestRead := false
	if err := ts.ReadFile(ctx, path.Join(dir, manifestName), nil, &buf); err == nil {
		manifestRead = true
		_ = json.Unmarshal([]byte(buf.String()), &previous)
	}
	if len(skills) == 0 && len(previous) == 0 {
		// Nothing to install and nothing previously managed to remove: avoid
		// creating or rewriting the manifest on every session start.
		if !manifestRead || strings.TrimSpace(buf.String()) == "[]" {
			return nil
		}
	}
	current := map[string]bool{}
	names := []string{}
	for _, s := range skills {
		if !SkillName.MatchString(s.Name) {
			continue
		}
		current[s.Name] = true
		names = append(names, s.Name)
		if err := ts.WriteFile(ctx, path.Join(dir, s.Name, "SKILL.md"), 0o644, strings.NewReader(SkillMarkdown(s))); err != nil {
			return fmt.Errorf("write skill %s: %w", s.Name, err)
		}
	}
	for _, name := range previous {
		if !current[name] && SkillName.MatchString(name) {
			_ = ts.Remove(ctx, path.Join(dir, name), true)
		}
	}
	b, _ := json.Marshal(names)
	return ts.WriteFile(ctx, path.Join(dir, manifestName), 0o644, strings.NewReader(string(b)))
}
