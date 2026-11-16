//go:build !linux

package sandboxd

import (
	"log/slog"
	"syscall"
)

// harnessCgroup does nothing outside Linux: the process group kill is all there is.
type harnessCgroup struct{}

func newHarnessCgroup(*slog.Logger) *harnessCgroup { return nil }

func newServiceCgroup(*slog.Logger, string) *harnessCgroup { return nil }

const serviceCgroupPrefix = "agenthold-svc-"

func (*harnessCgroup) path() string                { return "" }
func (*harnessCgroup) attach(*syscall.SysProcAttr) {}
func (*harnessCgroup) kill()                       {}
func (*harnessCgroup) remove()                     {}
