// Use and distribution licensed under the Apache license version 2.

//go:build linux

package rdmatopo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/jaypipes/ghw/pkg/linuxpath"
	"github.com/jaypipes/ghw/pkg/pci"
)

// Discover enumerates PCI on the local host via ghw and assembles
// the NVCX topology. ctx is threaded through both enumeration and
// VPD reads, so a ctx built with ghw.WithChroot (or GHW_CHROOT in
// the environment) points the whole pipeline at an alternate sysfs
// root. The sysfs PCI root is stat'd up front so callers can
// distinguish "no hardware" from "cannot read /sys" via
// ErrSysfsUnavailable.
func Discover(ctx context.Context) (*Topo, error) {
	sysfs := linuxpath.New(ctx).SysBusPciDevices
	if _, err := os.Stat(sysfs); err != nil {
		if os.IsNotExist(err) || errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("%w: %s", ErrSysfsUnavailable, sysfs)
		}
		return nil, fmt.Errorf("checking %s: %w", sysfs, err)
	}
	info, err := pci.New(ctx)
	if err != nil {
		return nil, err
	}
	return BuildTopology(ctx, info.Devices)
}
