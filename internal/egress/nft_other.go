//go:build !linux

package egress

import "errors"

// Install is unsupported outside Linux.
func (f *Firewall) Install() error {
	if err := f.validate(); err != nil {
		return err
	}
	return errors.New("the guest firewall requires Linux")
}

func updateGuestSet([]byte, bool) error { return errors.New("the guest firewall requires Linux") }
