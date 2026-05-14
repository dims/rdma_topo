// Use and distribution licensed under the Apache license version 2.

//go:build !linux

package rdmatopo

import (
	"context"
	"errors"
)

// Discover is not supported on non-Linux platforms; the rdma_topo
// data sources live under /sys/bus/pci.
func Discover(_ context.Context) (*Topo, error) {
	return nil, errors.New("rdmatopo: Discover is only supported on Linux")
}
