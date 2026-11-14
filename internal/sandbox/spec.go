package sandbox

import "github.com/gempir/agenthold/internal/store/db"

// BaseSpec builds a sandbox spec from the agent's settings. sandboxd is the
// entrypoint of every sandbox; drivers make it available at SandboxdDir.
func BaseSpec(a db.Agent) Spec {
	return Spec{
		AgentID:    a.ID,
		Image:      a.Image,
		Resources:  Resources{CPUs: a.Cpus, MemoryMB: a.MemoryMb},
		Entrypoint: []string{SandboxdDir + "/sandboxd"},
		Cmd:        []string{},
	}
}
