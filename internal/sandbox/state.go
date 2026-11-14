package sandbox

// Phase is the persisted lifecycle state of an agent's sandbox.
type Phase string

// Phases. Transient phases end in "-ing"; the manager resolves them after a
// crash by asking the driver (see Manager.Reconcile).
const (
	PhaseCreating   Phase = "creating"
	PhaseStarting   Phase = "starting"
	PhaseRunning    Phase = "running"
	PhaseStopping   Phase = "stopping"
	PhaseSleeping   Phase = "sleeping"
	PhaseRebuilding Phase = "rebuilding"
	PhaseDestroying Phase = "destroying"
	PhaseError      Phase = "error"
)

// Transient reports whether an operation is in progress.
func (p Phase) Transient() bool {
	switch p {
	case PhaseCreating, PhaseStarting, PhaseStopping, PhaseRebuilding, PhaseDestroying:
		return true
	}
	return false
}

// Allowed source phases for each operation. Anything else is a conflict.
var (
	startFrom   = []Phase{PhaseSleeping, PhaseError}
	stopFrom    = []Phase{PhaseRunning, PhaseError}
	rebuildFrom = []Phase{PhaseRunning, PhaseSleeping, PhaseError}
	destroyFrom = []Phase{PhaseRunning, PhaseSleeping, PhaseError, PhaseCreating, PhaseStarting, PhaseStopping, PhaseRebuilding}
)

func phases(ps []Phase) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}
