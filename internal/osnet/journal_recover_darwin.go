//go:build darwin

package osnet

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

func platformRecoverJournal(ctx context.Context, journal ownershipJournal) error {
	var errs []error
	physical := append(routeStrings(journal.ServerAddress), journal.ExcludeRoutes...)
	physical = append(physical, hostRouteStrings(journal.DynamicExclude)...)
	for _, raw := range physical {
		if err := deleteDarwinOwnedRoute(ctx, raw, journal.Gateway); err != nil {
			errs = append(errs, err)
		}
	}
	for _, raw := range append(journal.IncludeRoutes, hostRouteStrings(journal.DynamicInclude)...) {
		if err := deleteDarwinOwnedRoute(ctx, raw, strings.Split(journal.VPNAddress, "/")[0]); err != nil {
			errs = append(errs, err)
		}
	}
	if len(journal.DNSServers) > 0 {
		if err := clearDarwinDNS(ctx); err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such key") {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func deleteDarwinOwnedRoute(ctx context.Context, raw, expectedGateway string) error {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "route", "-n", "get", prefix.Addr().String()).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "not in table") {
			return nil
		}
		return fmt.Errorf("inspect route %s: %w: %s", prefix, err, strings.TrimSpace(string(out)))
	}
	gateway := ""
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && strings.TrimSuffix(fields[0], ":") == "gateway" {
			gateway = fields[1]
			break
		}
	}
	if gateway != expectedGateway {
		return nil
	}
	return run(ctx, "route", "delete", "-net", prefix.String())
}

func routeStrings(address string) []string {
	if address == "" {
		return nil
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return []string{address}
	}
	return []string{netip.PrefixFrom(addr, addr.BitLen()).String()}
}

func hostRouteStrings(addresses []string) []string {
	out := make([]string, 0, len(addresses))
	for _, raw := range addresses {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			out = append(out, raw)
		} else {
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()).String())
		}
	}
	return out
}
