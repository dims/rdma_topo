// Use and distribution licensed under the Apache license version 2.

// Package topotest builds synthetic NVCX topology fixtures shared by
// rdmatopo's own tests and the report subpackage's tests. The
// fixtures are constructed entirely from in-memory *pci.Device
// values so callers do not need a real (or chroot'd) sysfs.
package topotest

import (
	"context"
	"testing"

	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/ghw/pkg/topology"
	"github.com/jaypipes/pcidb"

	rdmatopo "github.com/dims/rdma_topo"
)

// VPDFixture collects synthetic VPD records keyed by PCI address so
// tests can inject them via rdmatopo.WithVPDReader without mutating
// the underlying *pci.Device.
type VPDFixture map[string]*pci.VPD

// Reader returns a VPDReader that resolves devices through this
// fixture. Devices with no entry return nil VPD (no error), which
// the pairing logic treats as "no V3 keyword".
func (f VPDFixture) Reader() rdmatopo.VPDReader {
	return func(_ context.Context, d *pci.Device) (*pci.VPD, error) {
		if d == nil {
			return nil, nil
		}
		return f[d.Address], nil
	}
}

// AddV3 records a VPD entry containing only the supplied V3 keyword.
func (f VPDFixture) AddV3(addr, v3 string) {
	if v3 == "" {
		return
	}
	f[addr] = &pci.VPD{ReadOnly: map[string]string{"V3": v3}}
}

// FakeDev builds a *pci.Device with a fixed address, vendor/product/
// class identifiers, and (optionally) registers a VPD V3 keyword
// into the supplied fixture for later injection via WithVPDReader.
func FakeDev(f VPDFixture, addr, vendor, product, class, subclass, progif, v3 string) *pci.Device {
	d := &pci.Device{
		Address:              addr,
		Vendor:               &pcidb.Vendor{ID: vendor},
		Product:              &pcidb.Product{ID: product},
		Class:                &pcidb.Class{ID: class},
		Subclass:             &pcidb.Subclass{ID: subclass},
		ProgrammingInterface: &pcidb.ProgrammingInterface{ID: progif},
	}
	f.AddV3(addr, v3)
	return d
}

// LinkParent sets parent <-> child pointers, mirroring what ghw's
// post-enumeration linkDeviceTree pass does at runtime.
func LinkParent(child, parent *pci.Device) {
	child.Parent = parent
	child.ParentAddress = parent.Address
	parent.Children = append(parent.Children, child)
}

// SampleHandles names the devices produced by BuildSampleComplex so
// tests can assert on individual nodes of the canonical topology:
//
//	GraceRP --> CXUSP -> CXDMADsp -> CXDMA
//	                  -> NVGPUDsp1 -> NVGPUUSP2 -> NVGPUDsp2 -> NVGPU
//	CXPF       (independent root, paired with CXDMA via VPD V3)
type SampleHandles struct {
	GraceRP, CXUSP                  *pci.Device
	CXDMADsp, CXDMA                 *pci.Device
	NVGPUDsp1, NVGPUUSP2, NVGPUDsp2 *pci.Device
	NVGPU                           *pci.Device
	CXPF                            *pci.Device
}

// BuildSampleComplex synthesizes the canonical NVCX topology
// described at rdma_topo:315-318. Returns the device list, named
// handles for assertion, and the VPD fixture so callers can inject
// VPD via rdmatopo.WithVPDReader.
func BuildSampleComplex() ([]*pci.Device, *SampleHandles, VPDFixture) {
	f := VPDFixture{}
	h := &SampleHandles{
		GraceRP:   FakeDev(f, "0000:00:00.0", "10de", "22b1", "06", "04", "00", ""),
		CXUSP:     FakeDev(f, "0000:01:00.0", "15b3", "197c", "06", "04", "00", ""),
		CXDMADsp:  FakeDev(f, "0000:02:00.0", "15b3", "197c", "06", "04", "00", ""),
		CXDMA:     FakeDev(f, "0000:03:00.0", "15b3", "2100", "08", "80", "00", "test-uuid-1"),
		NVGPUDsp1: FakeDev(f, "0000:02:01.0", "15b3", "197c", "06", "04", "00", ""),
		NVGPUUSP2: FakeDev(f, "0000:04:00.0", "15b3", "197c", "06", "04", "00", ""),
		NVGPUDsp2: FakeDev(f, "0000:05:00.0", "15b3", "197c", "06", "04", "00", ""),
		NVGPU:     FakeDev(f, "0000:06:00.0", "10de", "2330", "03", "02", "00", ""),
		CXPF:      FakeDev(f, "0000:07:00.0", "15b3", "1023", "02", "00", "00", "test-uuid-1"),
	}
	LinkParent(h.CXUSP, h.GraceRP)
	LinkParent(h.CXDMADsp, h.CXUSP)
	LinkParent(h.CXDMA, h.CXDMADsp)
	LinkParent(h.NVGPUDsp1, h.CXUSP)
	LinkParent(h.NVGPUUSP2, h.NVGPUDsp1)
	LinkParent(h.NVGPUDsp2, h.NVGPUUSP2)
	LinkParent(h.NVGPU, h.NVGPUDsp2)

	devs := []*pci.Device{
		h.GraceRP, h.CXUSP, h.CXDMADsp, h.CXDMA,
		h.NVGPUDsp1, h.NVGPUUSP2, h.NVGPUDsp2, h.NVGPU,
		h.CXPF,
	}
	return devs, h, f
}

// BuildSampleTopo wraps BuildSampleComplex with an assembled
// rdmatopo.Topo so report tests can exercise the formatters against
// realistic state. The boolean flags layer optional data onto the
// fixture: a VPD Identifier on the cx_pf, a NUMA node, and an extra
// NVMe under cx_pf's root.
func BuildSampleTopo(t *testing.T, withVPDName, withNUMA, addNVMe bool) *rdmatopo.Topo {
	t.Helper()
	devs, h, f := BuildSampleComplex()
	if withVPDName {
		f[h.CXPF.Address] = &pci.VPD{
			Identifier: "ConnectX-8 NIC sample",
			ReadOnly:   map[string]string{"V3": "test-uuid-1"},
		}
	}
	if withNUMA {
		h.CXPF.Node = &topology.Node{ID: 3}
	}
	if addNVMe {
		nvme := FakeDev(f, "0000:0a:00.0", "144d", "a804", "01", "08", "02", "")
		LinkParent(nvme, h.CXPF)
		devs = append(devs, nvme)
	}
	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err != nil {
		t.Fatalf("BuildTopology returned err: %v", err)
	}
	return topo
}
