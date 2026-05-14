// Use and distribution licensed under the Apache license version 2.

package rdmatopo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jaypipes/ghw/pkg/pci"
)

// ErrNoCXDMA is returned by BuildTopology when no ConnectX DMA Direct
// function is present on the system. Mirrors the Python "No ConnectX
// DMA Direct functions detected" error at rdma_topo:500.
var ErrNoCXDMA = errors.New("no ConnectX DMA Direct functions detected")

// ErrVPDPermissionDenied is returned when reading a cx_nic or
// cx_dma VPD file is denied (the sysfs vpd file is typically
// root-only). Mirrors Python's "are you root?" at rdma_topo:165-167.
var ErrVPDPermissionDenied = errors.New("permission denied reading PCI VPD; try running as root")

// ErrSysfsUnavailable is returned by callers when the sysfs PCI
// devices root is missing or unreadable, so that "no hardware" can
// be distinguished from "cannot read /sys".
var ErrSysfsUnavailable = errors.New("sysfs PCI devices root not readable")

// VPDReader is the function signature used by BuildTopology to fetch
// the Vital Product Data block for a single PCI device. The default
// implementation invokes (*pci.Device).VPD(ctx) directly; tests can
// pass a synthetic reader via WithVPDReader to exercise VPD-dependent
// code paths without a real sysfs.
type VPDReader func(context.Context, *pci.Device) (*pci.VPD, error)

// defaultVPDReader reads VPD from the device's sysfs entry.
func defaultVPDReader(ctx context.Context, d *pci.Device) (*pci.VPD, error) {
	return d.VPD(ctx)
}

// Option configures BuildTopology behavior.
type Option func(*options)

type options struct {
	vpdReader VPDReader
}

// WithVPDReader overrides the function used to fetch VPD for each
// device. The default invokes (*pci.Device).VPD(ctx).
func WithVPDReader(fn VPDReader) Option {
	return func(o *options) { o.vpdReader = fn }
}

// Topo is the result of classifying and walking the PCI device tree
// looking for ConnectX DMA Direct (NVCX) complexes.
type Topo struct {
	// All discovered NVCX complexes, sorted by the controlling NIC
	// PF's PCI address. May be empty even when HasCXDMA is true if
	// no complex could be fully assembled.
	Complexes []*NVCXComplex

	// HasCXDMA is true when at least one device in the input was
	// classified as cx_dma. When false, BuildTopology returns
	// ErrNoCXDMA.
	HasCXDMA bool

	// VPDReader is the function used by report helpers to resolve
	// each device's VPD. Captured at BuildTopology time so reports
	// see the same data the topology was built against. Library
	// consumers normally do not need to touch this; pass
	// WithVPDReader to BuildTopology if injection is needed.
	VPDReader VPDReader
}

// ComplexFor returns the NVCX complex that contains the device with
// the supplied PCI address (a cx_pf, cx_dma, GPU, or NVMe), or nil
// if no complex includes it.
func (t *Topo) ComplexFor(bdf string) *NVCXComplex {
	if t == nil || bdf == "" {
		return nil
	}
	for _, c := range t.Complexes {
		if c.CXDMA != nil && c.CXDMA.Address == bdf {
			return c
		}
		if c.NVGPU != nil && c.NVGPU.Address == bdf {
			return c
		}
		for _, pf := range c.CXPFs {
			if pf != nil && pf.Address == bdf {
				return c
			}
		}
		for _, nvme := range c.NVMes {
			if nvme != nil && nvme.Address == bdf {
				return c
			}
		}
	}
	return nil
}

// NVCXComplex collects the related PCI functions that together form
// a single ConnectX GPU Direct path: one or more NIC PFs, the DMA
// Direct function paired with them via VPD V3 UUID, the on-path GPU,
// and any nearby NVMe storage devices. Mirrors rdma_topo.NVCX_Complex
// at rdma_topo:228.
type NVCXComplex struct {
	// CXPFs is the set of NIC PFs that share the same VPD V3 UUID as
	// the CX DMA function. Sorted by PCI address.
	CXPFs []*pci.Device
	// CXPF is the primary PF (lowest-BDF entry in CXPFs).
	CXPF *pci.Device
	// CXDMA is the ConnectX DMA Direct function.
	CXDMA *pci.Device
	// NVGPU is the on-path NVIDIA GPU.
	NVGPU *pci.Device
	// NVMes are any NVMe storage devices found anywhere in the
	// full tree reachable from CXPF's root. Sorted by PCI address.
	NVMes []*pci.Device

	// SharedUSP is the cx_switch USP that sits in both the
	// CXDMA-to-root path and the NVGPU-to-root path.
	SharedUSP *pci.Device
	// CXDMADSP is the cx_switch DSP child of SharedUSP that leads
	// downstream toward CXDMA.
	CXDMADSP *pci.Device
	// NVGPUDSP is the cx_switch DSP child of SharedUSP that leads
	// downstream toward NVGPU.
	NVGPUDSP *pci.Device

	// vpdV3 is the VPD V3 UUID shared by the cx_dma and the cx_nic
	// PFs in this complex, captured at assembly time. Exposed via
	// ID().
	vpdV3 string
}

// ID returns a stable identifier for this NVCX complex. The value
// is the VPD V3 UUID common to the cx_dma and cx_nic functions,
// hardware-assigned by the ConnectX firmware, suitable for use as
// a matching key (e.g. a Kubernetes DRA resource attribute) so
// independent agents on the same host can pair a GPU with the
// correct NIC.
func (c *NVCXComplex) ID() string {
	if c == nil {
		return ""
	}
	return c.vpdV3
}

// SubsystemSet collects the subsystem device names (e.g. ib0, mlx5_0,
// nvme0, card1) registered for the NIC PFs, the DMA function, the
// GPU, and any NVMe devices in this complex. Map key is the
// Linux system kind ("drm", "infiniband", "net", "nvme"); values are
// sorted device-name lists.
type SubsystemSet map[string][]string

// Subsystems aggregates pci.DeviceNamesByLinuxSystem output across
// every device in the complex. Mirrors NVCX_Complex.get_subsystems
// at rdma_topo:274.
func (c *NVCXComplex) Subsystems(ctx context.Context) SubsystemSet {
	out := SubsystemSet{}
	seen := map[string]map[string]struct{}{}
	add := func(d *pci.Device) {
		if d == nil {
			return
		}
		for kind, names := range pci.DeviceNamesByLinuxSystem(ctx, d) {
			set, ok := seen[kind]
			if !ok {
				set = map[string]struct{}{}
				seen[kind] = set
			}
			for _, n := range names {
				set[n] = struct{}{}
			}
		}
	}
	for _, pf := range c.CXPFs {
		add(pf)
	}
	add(c.CXDMA)
	add(c.NVGPU)
	for _, n := range c.NVMes {
		add(n)
	}
	for kind, set := range seen {
		out[kind] = sortedKeys(set)
	}
	return out
}

// BuildTopology classifies devs, pairs each cx_dma with its cx_nic
// via VPD V3 UUID, and walks the surrounding switch topology. It
// returns ErrNoCXDMA when devs has no cx_dma. Per-cx_dma assembly
// failures are accumulated in the returned error; healthy complexes
// still populate the Topo.
func BuildTopology(ctx context.Context, devs []*pci.Device, opts ...Option) (*Topo, error) {
	cfg := options{vpdReader: defaultVPDReader}
	for _, opt := range opts {
		opt(&cfg)
	}
	topo := &Topo{VPDReader: cfg.vpdReader}
	if len(devs) == 0 {
		return nil, ErrNoCXDMA
	}

	classified := make(map[*pci.Device]DeviceType, len(devs))
	var cxDMAs []*pci.Device
	for _, d := range devs {
		kind := Classify(d)
		classified[d] = kind
		if kind == TypeCXDMA {
			cxDMAs = append(cxDMAs, d)
		}
	}
	if len(cxDMAs) == 0 {
		return nil, ErrNoCXDMA
	}
	topo.HasCXDMA = true

	// Index cx_nic and cx_dma functions by VPD V3 UUID for pairing.
	// Permission errors short-circuit; other VPD errors fold into
	// the assembly error pile so healthy complexes still emit.
	vpdIdx, vpdErr := indexByVPDV3(ctx, devs, classified, cfg.vpdReader)
	if errors.Is(vpdErr, ErrVPDPermissionDenied) {
		return topo, vpdErr
	}

	var assembleErrs []error
	if vpdErr != nil {
		assembleErrs = append(assembleErrs, vpdErr)
	}
	for _, dma := range cxDMAs {
		c, err := assembleComplex(ctx, dma, vpdIdx, classified, cfg.vpdReader)
		if err != nil {
			assembleErrs = append(assembleErrs, fmt.Errorf("cx_dma %s: %w", dma.Address, err))
			continue
		}
		topo.Complexes = append(topo.Complexes, c)
	}
	sort.Slice(topo.Complexes, func(i, j int) bool {
		return topo.Complexes[i].CXPF.Address < topo.Complexes[j].CXPF.Address
	})

	if len(assembleErrs) > 0 {
		return topo, errors.Join(assembleErrs...)
	}
	return topo, nil
}

// indexByVPDV3 walks every cx_nic and cx_dma device, reads its VPD
// via the supplied reader, and returns a map of V3 keyword value to
// the list of devices sharing that value. Devices with no VPD or no
// V3 keyword are skipped.
func indexByVPDV3(ctx context.Context, devs []*pci.Device, classified map[*pci.Device]DeviceType, vpdReader VPDReader) (map[string][]*pci.Device, error) {
	out := map[string][]*pci.Device{}
	var errs []error
	for _, d := range devs {
		switch classified[d] {
		case TypeCXNIC, TypeCXDMA:
		default:
			continue
		}
		vpd, err := vpdReader(ctx, d)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil, fmt.Errorf("%w (at %s)", ErrVPDPermissionDenied, d.Address)
			}
			errs = append(errs, fmt.Errorf("vpd for %s: %w", d.Address, err))
			continue
		}
		if vpd == nil {
			continue
		}
		v3 := vpd.ReadOnly["V3"]
		if v3 == "" {
			continue
		}
		out[v3] = append(out[v3], d)
	}
	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}

// assembleComplex implements PCITopo.__get_nvcx_complex
// (rdma_topo:313-371) for a single cx_dma device.
func assembleComplex(ctx context.Context, dma *pci.Device, vpdIdx map[string][]*pci.Device, classified map[*pci.Device]DeviceType, vpdReader VPDReader) (*NVCXComplex, error) {
	vpd, err := vpdReader(ctx, dma)
	if err != nil {
		return nil, fmt.Errorf("vpd: %w", err)
	}
	if vpd == nil || vpd.ReadOnly["V3"] == "" {
		return nil, errors.New("cx_dma has no VPD V3 UUID")
	}
	v3 := vpd.ReadOnly["V3"]
	pfsAll := vpdIdx[v3]
	if len(pfsAll) == 0 {
		return nil, errors.New("no PF with matching VPD V3 UUID")
	}

	// PFs are every cx_nic sharing V3. Filter by classified type so a
	// second cx_dma sharing V3 (corner case) can't be picked as PF.
	var pfs []*pci.Device
	for _, p := range pfsAll {
		if classified[p] == TypeCXNIC {
			pfs = append(pfs, p)
		}
	}
	if len(pfs) == 0 {
		return nil, errors.New("no PF with matching VPD V3 UUID")
	}
	sort.Slice(pfs, func(i, j int) bool {
		return pfs[i].Address < pfs[j].Address
	})

	// Walk up from cx_dma: cx_switch -> cx_switch -> grace_rp.
	dmaDSP := requireParentType(dma, TypeCXSwitch, classified)
	if dmaDSP == nil {
		return nil, errors.New("unrecognized upstream path: parent of cx_dma is not cx_switch")
	}
	cxUSP := requireParentType(dmaDSP, TypeCXSwitch, classified)
	if cxUSP == nil {
		return nil, errors.New("unrecognized upstream path: grandparent of cx_dma is not cx_switch")
	}
	graceRP := requireParentType(cxUSP, TypeGraceRP, classified)
	if graceRP == nil {
		return nil, errors.New("unrecognized upstream path: cx_dma not under a grace_rp")
	}

	// Find the unique NVGPU anywhere under grace_rp.
	var nvgpus []*pci.Device
	walkDownstream(graceRP, func(d *pci.Device) {
		if classified[d] == TypeNVGPU {
			nvgpus = append(nvgpus, d)
		}
	})
	if len(nvgpus) != 1 {
		return nil, fmt.Errorf("expected exactly 1 nvgpu under grace_rp, found %d", len(nvgpus))
	}
	gpu := nvgpus[0]

	// Walk up from the GPU: three cx_switch hops should land at cxUSP.
	gpuDSP2 := requireParentType(gpu, TypeCXSwitch, classified)
	if gpuDSP2 == nil {
		return nil, errors.New("unrecognized upstream path from gpu: parent not cx_switch")
	}
	gpuUSP2 := requireParentType(gpuDSP2, TypeCXSwitch, classified)
	if gpuUSP2 == nil {
		return nil, errors.New("unrecognized upstream path from gpu: grandparent not cx_switch")
	}
	gpuDSP1 := requireParentType(gpuUSP2, TypeCXSwitch, classified)
	if gpuDSP1 == nil {
		return nil, errors.New("unrecognized upstream path from gpu: great-grandparent not cx_switch")
	}
	if requireParentType(gpuDSP1, TypeCXSwitch, classified) != cxUSP {
		return nil, errors.New("gpu upstream path does not converge on cx_dma's USP")
	}

	// Sanity check: the exact set of devices reachable from grace_rp
	// must match what we expect.
	expected := map[*pci.Device]struct{}{
		dma: {}, dmaDSP: {}, cxUSP: {},
		gpu: {}, gpuDSP2: {}, gpuUSP2: {}, gpuDSP1: {},
	}
	actual := map[*pci.Device]struct{}{}
	walkDownstream(graceRP, func(d *pci.Device) {
		actual[d] = struct{}{}
	})
	if !setsEqual(expected, actual) {
		return nil, errors.New("unexpected extra devices in nvcx topology under grace_rp")
	}

	c := &NVCXComplex{
		CXPFs: pfs,
		CXPF:  pfs[0],
		CXDMA: dma,
		NVGPU: gpu,
		vpdV3: v3,
	}

	// Find the shared USP: the cx_switch that appears in both upstream
	// paths. Its DSP children are dmaDSP and nvgpu's DSP.
	c.SharedUSP, c.CXDMADSP, c.NVGPUDSP = findSharedUSP(dma, gpu, classified)

	// Collect NVMe siblings: anything under the same top-level root
	// as cx_pf with classified type nvme.
	root := topRoot(c.CXPF)
	walkDownstream(root, func(d *pci.Device) {
		if classified[d] == TypeNVMe {
			c.NVMes = append(c.NVMes, d)
		}
	})
	sort.Slice(c.NVMes, func(i, j int) bool {
		return c.NVMes[i].Address < c.NVMes[j].Address
	})

	return c, nil
}

// requireParentType returns d.Parent iff it is non-nil and classified
// as the requested type, else nil. Mirrors rdma_topo.check_parent at
// rdma_topo:282.
func requireParentType(d *pci.Device, want DeviceType, classified map[*pci.Device]DeviceType) *pci.Device {
	if d == nil || d.Parent == nil {
		return nil
	}
	if classified[d.Parent] != want {
		return nil
	}
	return d.Parent
}

// walkDownstream invokes fn for every descendant of root (not
// including root itself), in pre-order.
func walkDownstream(root *pci.Device, fn func(*pci.Device)) {
	for _, c := range root.Children {
		fn(c)
		walkDownstream(c, fn)
	}
}

// upstreamPath returns the list of devices from d.Parent up to the
// root (exclusive of d itself). Order is parent, grandparent, ....
func upstreamPath(d *pci.Device) []*pci.Device {
	var out []*pci.Device
	for p := d.Parent; p != nil; p = p.Parent {
		out = append(out, p)
	}
	return out
}

// topRoot returns the topmost ancestor of d. Mirrors the "if not
// pdev.parent" check at rdma_topo:149.
func topRoot(d *pci.Device) *pci.Device {
	cur := d
	for cur.Parent != nil {
		cur = cur.Parent
	}
	return cur
}

// findSharedUSP locates the cx_switch USP that appears on the
// upstream paths of both dma and gpu. Mirrors
// NVCX_Complex.__find_shared_usp at rdma_topo:258.
func findSharedUSP(dma, gpu *pci.Device, classified map[*pci.Device]DeviceType) (shared, dmaDSP, gpuDSP *pci.Device) {
	dmaPath := upstreamPath(dma)
	gpuSet := map[*pci.Device]struct{}{}
	for _, p := range upstreamPath(gpu) {
		gpuSet[p] = struct{}{}
	}
	for _, p := range dmaPath {
		if _, ok := gpuSet[p]; ok && classified[p] == TypeCXSwitch {
			shared = p
			break
		}
	}
	if shared == nil {
		return nil, nil, nil
	}
	childSet := map[*pci.Device]struct{}{}
	for _, c := range shared.Children {
		childSet[c] = struct{}{}
	}
	for _, p := range upstreamPath(dma) {
		if _, ok := childSet[p]; ok {
			dmaDSP = p
			break
		}
	}
	for _, p := range upstreamPath(gpu) {
		if _, ok := childSet[p]; ok {
			gpuDSP = p
			break
		}
	}
	return shared, dmaDSP, gpuDSP
}

func setsEqual(a, b map[*pci.Device]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
