// Package workflows implements an always-on durable multi-stage workflow engine
// supporting agent turns, sandbox shell commands, human-approval ask gates,
// cron schedules, inbound webhooks, and per-workflow concurrency limits.
package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/trigger"
)

// Workflow step kinds.
const (
	StepKindAgent = "agent"
	StepKindShell = "shell"
	StepKindAsk   = "ask"
)

// Workflow run statuses.
const (
	RunQueued     = "queued"
	RunRunning    = "running"
	RunNeedsInput = "needs_input"
	RunCompleted  = "completed"
	RunFailed     = "failed"
	RunCancelled  = "cancelled"
)

// Workflow step statuses.
const (
	StepPending   = "pending"
	StepRunning   = "running"
	StepWaiting   = "waiting"
	StepCompleted = "completed"
	StepFailed    = "failed"
	StepSkipped   = "skipped"
)

// Workflow trigger kinds.
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	TriggerWebhook  = "webhook"
)

var (
	// ErrInvalidDefinition means a workflow definition failed validation.
	ErrInvalidDefinition = errors.New("invalid workflow definition")
	// ErrNotFound means a workflow, run, or step was not found.
	ErrNotFound = errors.New("not found")
	// ErrConflict means an operation is not allowed in the current state.
	ErrConflict = errors.New("conflict")
	// ErrInvalidAnswer means an answer choice is missing or not allowed.
	ErrInvalidAnswer = errors.New("invalid answer")
)

var stepNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// StepDef defines one stage of a workflow pipeline.
type StepDef struct {
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	AgentID           string   `json:"agentId,omitempty"`
	Prompt            string   `json:"prompt,omitempty"`
	ReuseSession      bool     `json:"reuseSession,omitempty"`
	Command           string   `json:"command,omitempty"`
	Cwd               string   `json:"cwd,omitempty"`
	AskTitle          string   `json:"askTitle,omitempty"`
	AskChoices        []string `json:"askChoices,omitempty"`
	ContinueOnChoices []string `json:"continueOnChoices,omitempty"`
}

// Definition is the typed JSON definition persisted in workflows.definition_json.
type Definition struct {
	Steps            []StepDef `json:"steps"`
	Schedule         string    `json:"schedule,omitempty"`
	Timezone         string    `json:"timezone,omitempty"`
	ConcurrencyLimit int       `json:"concurrencyLimit,omitempty"`
	WebhookEnabled   bool      `json:"webhookEnabled,omitempty"`
	// WebhookTokenHash stores SHA-256 hex of the webhook bearer token; never returned to API clients.
	WebhookTokenHash string `json:"webhook_token_hash,omitempty"`
	// NextRunAt caches the next scheduled trigger time in unix seconds.
	NextRunAt int64 `json:"next_run_at,omitempty"`
}

// Validate checks a workflow definition and fills safe defaults.
func (d *Definition) Validate(now time.Time) error {
	if len(d.Steps) == 0 {
		return fmt.Errorf("%w: workflow must have at least one step", ErrInvalidDefinition)
	}
	if len(d.Steps) > 50 {
		return fmt.Errorf("%w: workflow cannot exceed 50 steps", ErrInvalidDefinition)
	}
	if d.ConcurrencyLimit < 0 {
		return fmt.Errorf("%w: concurrencyLimit must be >= 0", ErrInvalidDefinition)
	}
	seen := map[string]bool{}
	for i := range d.Steps {
		s := &d.Steps[i]
		s.Name = strings.TrimSpace(s.Name)
		if !stepNameRe.MatchString(s.Name) {
			return fmt.Errorf("%w: step %d name %q must match [a-zA-Z0-9][a-zA-Z0-9_-]{0,63}", ErrInvalidDefinition, i+1, s.Name)
		}
		key := strings.ToLower(s.Name)
		if seen[key] {
			return fmt.Errorf("%w: duplicate step name %q", ErrInvalidDefinition, s.Name)
		}
		seen[key] = true
		s.AgentID = strings.TrimSpace(s.AgentID)
		switch s.Kind {
		case StepKindAgent:
			if s.AgentID == "" {
				return fmt.Errorf("%w: agent step %q requires agentId", ErrInvalidDefinition, s.Name)
			}
			if strings.TrimSpace(s.Prompt) == "" {
				return fmt.Errorf("%w: agent step %q requires prompt", ErrInvalidDefinition, s.Name)
			}
		case StepKindShell:
			if s.AgentID == "" {
				return fmt.Errorf("%w: shell step %q requires agentId", ErrInvalidDefinition, s.Name)
			}
			if strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("%w: shell step %q requires command", ErrInvalidDefinition, s.Name)
			}
		case StepKindAsk:
			if strings.TrimSpace(s.AskTitle) == "" {
				return fmt.Errorf("%w: ask step %q requires askTitle", ErrInvalidDefinition, s.Name)
			}
			choices := make([]string, 0, len(s.AskChoices))
			for _, c := range s.AskChoices {
				if c = strings.TrimSpace(c); c != "" {
					choices = append(choices, c)
				}
			}
			if len(choices) == 0 {
				choices = []string{"Approve", "Reject"}
			}
			s.AskChoices = choices
		default:
			return fmt.Errorf("%w: step %q has unsupported kind %q", ErrInvalidDefinition, s.Name, s.Kind)
		}
	}
	d.Schedule = strings.TrimSpace(d.Schedule)
	d.Timezone = strings.TrimSpace(d.Timezone)
	if d.Schedule != "" {
		if d.Timezone == "" {
			d.Timezone = "UTC"
		}
		next, err := trigger.Next(d.Schedule, d.Timezone, now)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidDefinition, err)
		}
		d.NextRunAt = next.Unix()
	} else {
		d.NextRunAt = 0
	}
	return nil
}

// ParseDefinition unmarshals definition_json.
func ParseDefinition(raw string) (Definition, error) {
	var d Definition
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return Definition{}, fmt.Errorf("parse workflow definition: %w", err)
	}
	return d, nil
}

var templateVarRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

// RenderTemplate substitutes {{input}}, {{input.<key>}}, and
// {{steps.<name>.output}} placeholders inside prompts, shell commands, and ask
// titles. Unknown placeholders are left untouched.
func RenderTemplate(tmpl string, input map[string]any, stepOutputs map[string]string) string {
	if !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	var inputJSON string
	if len(input) > 0 {
		if b, err := json.Marshal(input); err == nil {
			inputJSON = string(b)
		}
	} else {
		inputJSON = "{}"
	}
	return templateVarRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		sub := templateVarRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		expr := sub[1]
		if expr == "input" {
			return inputJSON
		}
		if key, ok := strings.CutPrefix(expr, "input."); ok {
			if input != nil {
				if v, exists := input[key]; exists && v != nil {
					switch tv := v.(type) {
					case string:
						return tv
					default:
						if b, err := json.Marshal(tv); err == nil {
							return string(b)
						}
						return fmt.Sprint(tv)
					}
				}
			}
			return ""
		}
		if rest, ok := strings.CutPrefix(expr, "steps."); ok {
			if name, ok := strings.CutSuffix(rest, ".output"); ok {
				if out, exists := stepOutputs[name]; exists {
					return out
				}
				return ""
			}
		}
		return match
	})
}
