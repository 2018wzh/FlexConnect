package osnet

import (
	"errors"
	"fmt"
)

type dnsBackendAttempt struct {
	name string
	set  func() error
}

func chooseDNSBackend(attempts []dnsBackendAttempt) (string, error) {
	var errs []error
	for _, attempt := range attempts {
		if attempt.name == "" || attempt.set == nil {
			continue
		}
		if err := attempt.set(); err == nil {
			return attempt.name, nil
		} else {
			errs = append(errs, fmt.Errorf("%s: %w", attempt.name, err))
		}
	}
	return "", errors.Join(errs...)
}
