package sandbox

import (
	"fmt"

	"github.com/gempir/agenthold/internal/store/db"
)

// BaseSpec builds a sandbox spec from the agent's resource, base revision, and
// custom image layer settings.
func BaseSpec(a db.Agent) Spec {
	var layers []string
	if a.ImageID != "" && a.ImageRevision > 0 {
		layers = []string{fmt.Sprintf("image:%s:%d", a.ImageID, a.ImageRevision)}
	}
	return Spec{
		AgentID:   a.ID,
		Image:     a.Image,
		Layers:    layers,
		Resources: Resources{CPUs: a.Cpus, MemoryMB: a.MemoryMb},
	}
}
