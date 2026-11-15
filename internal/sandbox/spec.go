package sandbox

import "github.com/gempir/agenthold/internal/store/db"

// BaseSpec builds a sandbox spec from the agent's resource and base revision settings.
func BaseSpec(a db.Agent) Spec {
	return Spec{
		AgentID:   a.ID,
		Image:     a.Image,
		Resources: Resources{CPUs: a.Cpus, MemoryMB: a.MemoryMb},
	}
}
