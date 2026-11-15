package gen

import (
	"encoding/json"
	"errors"
	"strings"
)

// UnmarshalJSON rejects the obsolete per-agent image setting instead of
// silently accepting a configuration that cannot affect the sandbox.
func (a *AgentInput) UnmarshalJSON(data []byte) error {
	type input AgentInput
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		if strings.EqualFold(key, "image") {
			return errors.New("per-agent images are not supported")
		}
	}
	return json.Unmarshal(data, (*input)(a))
}

// UnmarshalJSON rejects the obsolete per-agent image setting on updates.
func (a *AgentPatch) UnmarshalJSON(data []byte) error {
	type patch AgentPatch
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		if strings.EqualFold(key, "image") {
			return errors.New("per-agent images are not supported")
		}
	}
	return json.Unmarshal(data, (*patch)(a))
}
