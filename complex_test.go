// Use and distribution licensed under the Apache license version 2.

package rdmatopo_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/jaypipes/ghw/pkg/pci"

	rdmatopo "github.com/dims/rdma_topo"
	"github.com/dims/rdma_topo/internal/topotest"
)

func TestBuildTopologyHappyPath(t *testing.T) {
	devs, h, f := topotest.BuildSampleComplex()

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err != nil {
		t.Fatalf("BuildTopology returned err: %v", err)
	}
	if !topo.HasCXDMA {
		t.Fatalf("HasCXDMA = false, want true")
	}
	if len(topo.Complexes) != 1 {
		t.Fatalf("got %d complexes, want 1", len(topo.Complexes))
	}
	c := topo.Complexes[0]
	if c.CXPF != h.CXPF {
		t.Errorf("CXPF = %v, want %v", c.CXPF, h.CXPF)
	}
	if c.CXDMA != h.CXDMA {
		t.Errorf("CXDMA = %v, want %v", c.CXDMA, h.CXDMA)
	}
	if c.NVGPU != h.NVGPU {
		t.Errorf("NVGPU = %v, want %v", c.NVGPU, h.NVGPU)
	}
	if c.SharedUSP != h.CXUSP {
		t.Errorf("SharedUSP = %v, want CXUSP", c.SharedUSP)
	}
	if c.CXDMADSP != h.CXDMADsp {
		t.Errorf("CXDMADSP = %v, want CXDMADsp", c.CXDMADSP)
	}
	if c.NVGPUDSP != h.NVGPUDsp1 {
		t.Errorf("NVGPUDSP = %v, want NVGPUDsp1", c.NVGPUDSP)
	}
}

func TestBuildTopologyNoCXDMA(t *testing.T) {
	// A tree with no cx_dma: just a Grace RP and a GPU.
	f := topotest.VPDFixture{}
	rp := topotest.FakeDev(f, "0000:00:00.0", "10de", "22b1", "06", "04", "00", "")
	gpu := topotest.FakeDev(f, "0000:01:00.0", "10de", "2330", "03", "02", "00", "")
	topotest.LinkParent(gpu, rp)
	_, err := rdmatopo.BuildTopology(context.Background(), []*pci.Device{rp, gpu}, rdmatopo.WithVPDReader(f.Reader()))
	if !errors.Is(err, rdmatopo.ErrNoCXDMA) {
		t.Fatalf("got err %v, want ErrNoCXDMA", err)
	}
}

func TestBuildTopologyEmptyInput(t *testing.T) {
	_, err := rdmatopo.BuildTopology(context.Background(), nil)
	if !errors.Is(err, rdmatopo.ErrNoCXDMA) {
		t.Fatalf("got err %v, want ErrNoCXDMA", err)
	}
}

func TestBuildTopologyVPDPermissionDenied(t *testing.T) {
	devs, h, _ := topotest.BuildSampleComplex()
	// Inject a permission error for the cx_dma VPD read. indexByVPDV3
	// should detect this and short-circuit with ErrVPDPermissionDenied
	// rather than letting it accumulate as a soft per-device error.
	reader := func(_ context.Context, d *pci.Device) (*pci.VPD, error) {
		if d.Address == h.CXDMA.Address {
			return nil, fs.ErrPermission
		}
		return &pci.VPD{ReadOnly: map[string]string{"V3": "test-uuid-1"}}, nil
	}

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(reader))
	if !errors.Is(err, rdmatopo.ErrVPDPermissionDenied) {
		t.Fatalf("got err %v, want ErrVPDPermissionDenied", err)
	}
	if topo != nil && len(topo.Complexes) != 0 {
		t.Errorf("got %d complexes, want 0 (early exit)", len(topo.Complexes))
	}
}

func TestTopoComplexFor(t *testing.T) {
	devs, h, f := topotest.BuildSampleComplex()
	nvme := topotest.FakeDev(f, "0000:0a:00.0", "144d", "a804", "01", "08", "02", "")
	topotest.LinkParent(nvme, h.CXPF)
	devs = append(devs, nvme)

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	want := topo.Complexes[0]
	for _, addr := range []string{h.CXPF.Address, h.CXDMA.Address, h.NVGPU.Address, nvme.Address} {
		if got := topo.ComplexFor(addr); got != want {
			t.Errorf("ComplexFor(%s) = %v, want %v", addr, got, want)
		}
	}
	if got := topo.ComplexFor("0000:ff:ff.7"); got != nil {
		t.Errorf("ComplexFor(unknown bdf) = %v, want nil", got)
	}
	if got := topo.ComplexFor(""); got != nil {
		t.Errorf("ComplexFor(empty) = %v, want nil", got)
	}
	var nilT *rdmatopo.Topo
	if got := nilT.ComplexFor(h.CXPF.Address); got != nil {
		t.Errorf("nil receiver ComplexFor = %v, want nil", got)
	}
}

func TestNVCXComplexID(t *testing.T) {
	devs, _, f := topotest.BuildSampleComplex()
	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if got := topo.Complexes[0].ID(); got != "test-uuid-1" {
		t.Errorf("ID() = %q, want test-uuid-1 (the V3 used for pairing)", got)
	}
	var nilC *rdmatopo.NVCXComplex
	if got := nilC.ID(); got != "" {
		t.Errorf("nil receiver ID() = %q, want empty", got)
	}
}

func TestBuildTopologyContinuesOnSoftVPDError(t *testing.T) {
	// Add an orphan cx_dma whose VPD read fails with a parse error.
	// The healthy sample complex must still emit; the orphan must
	// surface as a soft error alongside it.
	devs, h, f := topotest.BuildSampleComplex()
	orphan := topotest.FakeDev(f, "0000:0b:00.0", "15b3", "2100", "08", "80", "00", "")
	devs = append(devs, orphan)

	reader := func(_ context.Context, d *pci.Device) (*pci.VPD, error) {
		if d.Address == orphan.Address {
			return nil, errors.New("synthetic VPD parse failure")
		}
		return f[d.Address], nil
	}

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(reader))
	if err == nil {
		t.Fatal("expected soft error for orphan, got nil")
	}
	if topo == nil || len(topo.Complexes) != 1 {
		t.Fatalf("expected 1 healthy complex, got %d", len(topo.Complexes))
	}
	if topo.Complexes[0].CXPF != h.CXPF {
		t.Errorf("expected canonical cx_pf, got %v", topo.Complexes[0].CXPF)
	}
	if !strings.Contains(err.Error(), orphan.Address) {
		t.Errorf("expected soft error to mention orphan %s, got: %v", orphan.Address, err)
	}
}

func TestBuildTopologyFiltersCXPFByType(t *testing.T) {
	devs, _, f := topotest.BuildSampleComplex()
	// Orphan cx_dma sharing the real complex's V3. Its address sorts
	// before the real cx_pf (0000:07:00.0), so before the type filter
	// it would have been picked as primary cx_pf.
	extraDMA := topotest.FakeDev(f, "0000:00:01.0", "15b3", "2100", "08", "80", "00", "test-uuid-1")
	devs = append(devs, extraDMA)

	topo, _ := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if topo == nil || len(topo.Complexes) != 1 {
		t.Fatalf("expected 1 complex, got %d", len(topo.Complexes))
	}
	if got := topo.Complexes[0].CXPF.Address; got != "0000:07:00.0" {
		t.Errorf("CXPF = %q, want 0000:07:00.0 (cx_nic); dma leaked through as PF", got)
	}
}

func TestBuildTopologyDMAWithoutMatchingPF(t *testing.T) {
	// Replace the PF's V3 UUID with a mismatched value so VPD
	// pairing fails.
	devs, h, f := topotest.BuildSampleComplex()
	f.AddV3(h.CXPF.Address, "different-uuid")

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err == nil {
		t.Fatalf("expected error, got nil; topo=%+v", topo)
	}
	if topo == nil || len(topo.Complexes) != 0 {
		t.Errorf("expected 0 complexes assembled, got %d", len(topo.Complexes))
	}
}

func TestBuildTopologyMultipleGPUsUnderGraceRP(t *testing.T) {
	devs, h, f := topotest.BuildSampleComplex()
	// Attach a second GPU directly to the grace_rp: this should
	// cause assembleComplex to bail with "exactly 1 nvgpu" error.
	extraGPU := topotest.FakeDev(f, "0000:08:00.0", "10de", "2331", "03", "02", "00", "")
	topotest.LinkParent(extraGPU, h.GraceRP)
	devs = append(devs, extraGPU)

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err == nil {
		t.Fatalf("expected error from extra GPU, got nil")
	}
	if len(topo.Complexes) != 0 {
		t.Errorf("expected 0 complexes assembled, got %d", len(topo.Complexes))
	}
}

func TestBuildTopologyUnexpectedDeviceUnderGraceRP(t *testing.T) {
	devs, h, f := topotest.BuildSampleComplex()
	// Attach a random NVMe device to the cx_usp (well inside the
	// grace_rp subtree). The sanity check on the exact reachable
	// set should reject this.
	stray := topotest.FakeDev(f, "0000:09:00.0", "144d", "a804", "01", "08", "02", "")
	topotest.LinkParent(stray, h.CXUSP)
	devs = append(devs, stray)

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err == nil {
		t.Fatalf("expected error from stray device, got nil")
	}
	if len(topo.Complexes) != 0 {
		t.Errorf("expected 0 complexes assembled, got %d", len(topo.Complexes))
	}
}

func TestBuildTopologyDMAParentNotCXSwitch(t *testing.T) {
	// cx_dma sitting directly under a grace_rp with no switch in
	// between: must fail the parent-type check.
	f := topotest.VPDFixture{}
	rp := topotest.FakeDev(f, "0000:00:00.0", "10de", "22b1", "06", "04", "00", "")
	dma := topotest.FakeDev(f, "0000:01:00.0", "15b3", "2100", "08", "80", "00", "uuid")
	pf := topotest.FakeDev(f, "0000:02:00.0", "15b3", "1023", "02", "00", "00", "uuid")
	topotest.LinkParent(dma, rp)

	topo, err := rdmatopo.BuildTopology(context.Background(), []*pci.Device{rp, dma, pf}, rdmatopo.WithVPDReader(f.Reader()))
	if err == nil {
		t.Fatalf("expected error from missing switch, got nil")
	}
	if len(topo.Complexes) != 0 {
		t.Errorf("expected 0 complexes assembled, got %d", len(topo.Complexes))
	}
}

func TestBuildTopologyCollectsNVMes(t *testing.T) {
	devs, h, f := topotest.BuildSampleComplex()
	// Attach an NVMe under the CXPF's root (it has no parent, so the
	// NVMe is its child).
	nvme := topotest.FakeDev(f, "0000:0a:00.0", "144d", "a804", "01", "08", "02", "")
	topotest.LinkParent(nvme, h.CXPF)
	devs = append(devs, nvme)

	topo, err := rdmatopo.BuildTopology(context.Background(), devs, rdmatopo.WithVPDReader(f.Reader()))
	if err != nil {
		t.Fatalf("BuildTopology returned err: %v", err)
	}
	if len(topo.Complexes) != 1 {
		t.Fatalf("got %d complexes, want 1", len(topo.Complexes))
	}
	if len(topo.Complexes[0].NVMes) != 1 || topo.Complexes[0].NVMes[0] != nvme {
		t.Errorf("NVMes = %v, want [%v]", topo.Complexes[0].NVMes, nvme)
	}
}

func TestBuildTopologyComplexesSortedByPFAddress(t *testing.T) {
	// Build two independent NVCX complexes with different PF
	// addresses and confirm output sort order.
	devsA, hA, fA := topotest.BuildSampleComplex()
	// Shift second complex into a different segment so addresses
	// don't collide.
	fB := topotest.VPDFixture{}
	hB := &topotest.SampleHandles{
		GraceRP:   topotest.FakeDev(fB, "0001:00:00.0", "10de", "22b1", "06", "04", "00", ""),
		CXUSP:     topotest.FakeDev(fB, "0001:01:00.0", "15b3", "197c", "06", "04", "00", ""),
		CXDMADsp:  topotest.FakeDev(fB, "0001:02:00.0", "15b3", "197c", "06", "04", "00", ""),
		CXDMA:     topotest.FakeDev(fB, "0001:03:00.0", "15b3", "2100", "08", "80", "00", "test-uuid-2"),
		NVGPUDsp1: topotest.FakeDev(fB, "0001:02:01.0", "15b3", "197c", "06", "04", "00", ""),
		NVGPUUSP2: topotest.FakeDev(fB, "0001:04:00.0", "15b3", "197c", "06", "04", "00", ""),
		NVGPUDsp2: topotest.FakeDev(fB, "0001:05:00.0", "15b3", "197c", "06", "04", "00", ""),
		NVGPU:     topotest.FakeDev(fB, "0001:06:00.0", "10de", "2330", "03", "02", "00", ""),
		CXPF:      topotest.FakeDev(fB, "0001:07:00.0", "15b3", "1023", "02", "00", "00", "test-uuid-2"),
	}
	topotest.LinkParent(hB.CXUSP, hB.GraceRP)
	topotest.LinkParent(hB.CXDMADsp, hB.CXUSP)
	topotest.LinkParent(hB.CXDMA, hB.CXDMADsp)
	topotest.LinkParent(hB.NVGPUDsp1, hB.CXUSP)
	topotest.LinkParent(hB.NVGPUUSP2, hB.NVGPUDsp1)
	topotest.LinkParent(hB.NVGPUDsp2, hB.NVGPUUSP2)
	topotest.LinkParent(hB.NVGPU, hB.NVGPUDsp2)
	devsB := []*pci.Device{
		hB.GraceRP, hB.CXUSP, hB.CXDMADsp, hB.CXDMA,
		hB.NVGPUDsp1, hB.NVGPUUSP2, hB.NVGPUDsp2, hB.NVGPU, hB.CXPF,
	}
	// Merge fixtures and feed B first to verify result sort is by
	// PF address, not input order.
	merged := append([]*pci.Device{}, devsB...)
	merged = append(merged, devsA...)
	for k, v := range fA {
		fB[k] = v
	}

	topo, err := rdmatopo.BuildTopology(context.Background(), merged, rdmatopo.WithVPDReader(fB.Reader()))
	if err != nil {
		t.Fatalf("BuildTopology returned err: %v", err)
	}
	if len(topo.Complexes) != 2 {
		t.Fatalf("got %d complexes, want 2", len(topo.Complexes))
	}
	if topo.Complexes[0].CXPF != hA.CXPF {
		t.Errorf("first complex CXPF = %v, want %v (lower address)", topo.Complexes[0].CXPF, hA.CXPF)
	}
	if topo.Complexes[1].CXPF != hB.CXPF {
		t.Errorf("second complex CXPF = %v, want %v", topo.Complexes[1].CXPF, hB.CXPF)
	}
}
