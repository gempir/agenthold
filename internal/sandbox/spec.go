package sandbox

import "github.com/gempir/agenthold/internal/store/db"

// BaseSpec builds the parts of a spec that come from the agent's settings.
// Until sandboxd exists (M3) the entrypoint just keeps the sandbox alive.
func BaseSpec(a db.Agent) Spec {
	return Spec{
		AgentID:    a.ID,
		Image:      a.Image,
		Resources:  Resources{CPUs: a.Cpus, MemoryMB: a.MemoryMb},
		Entrypoint: []string{"sleep", "infinity"},
		Cmd:        []string{},
	}
}
