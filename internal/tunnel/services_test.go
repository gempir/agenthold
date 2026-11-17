package tunnel

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// An old sandboxd that does not know the service, archive and exec ops
// fails fast with ErrTooOld.
func TestNewOpsOnOldGuest(t *testing.T) {
	s, opens := fakeGuest(t, nil)
	go func() {
		for range opens {
		}
	}()
	ctx := context.Background()
	if _, err := s.Services(ctx, ServiceRequest{Action: ServiceStatus}); !errors.Is(err, ErrTooOld) {
		t.Fatalf("services: %v", err)
	}
	if err := s.ServiceLogs(ctx, "x", 1, false, &bytes.Buffer{}); !errors.Is(err, ErrTooOld) {
		t.Fatalf("logs: %v", err)
	}
	if err := s.Tar(ctx, "x", nil, 0, &bytes.Buffer{}); !errors.Is(err, ErrTooOld) {
		t.Fatalf("tar: %v", err)
	}
	if err := s.Untar(ctx, "x", 0, bytes.NewReader(nil)); !errors.Is(err, ErrTooOld) {
		t.Fatalf("untar: %v", err)
	}
	if err := s.Symlink(ctx, "a", "b"); !errors.Is(err, ErrTooOld) {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := s.Exec(ctx, []string{"true"}, "", nil, 1, 0); !errors.Is(err, ErrTooOld) {
		t.Fatalf("exec: %v", err)
	}
}
