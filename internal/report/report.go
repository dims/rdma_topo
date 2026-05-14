// Use and distribution licensed under the Apache license version 2.

// Package report renders an assembled rdmatopo.Topo into JSON or
// human-readable text. It is a presentation layer over the
// rdma_topo library; data-only consumers (e.g. Kubernetes DRA
// drivers reading Topo.Complexes directly) do not need it.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jaypipes/ghw/pkg/pci"

	rdmatopo "github.com/dims/rdma_topo"
)

// jsonComplex is the per-complex shape emitted by JSON. It mirrors
// the Python topo_json output at rdma_topo:463 plus the additive
// nvcx_complex_id field carrying rdmatopo.NVCXComplex.ID().
type jsonComplex struct {
	RDMANICPFBDF   string                         `json:"rdma_nic_pf_bdf"`
	RDMADMABDF     string                         `json:"rdma_dma_bdf"`
	GPUBDF         string                         `json:"gpu_bdf"`
	Subsystems     map[string]map[string][]string `json:"subsystems"`
	NVCXComplexID  string                         `json:"nvcx_complex_id,omitempty"`
	RDMANICVPDName string                         `json:"rdma_nic_vpd_name,omitempty"`
	NUMANode       *int                           `json:"numa_node,omitempty"`
	NVMeBDF        string                         `json:"nvme_bdf,omitempty"`
}

// JSON writes the topology as indented JSON: an array of one
// object per NVCX complex, matching the Python topo_json shape plus
// an additive nvcx_complex_id field. The schema is stable;
// testdata/topo.golden.json and TestJSONMatchesGoldenFixture pin
// the bytes. Optional fields (struct tag `omitempty`) drop when
// empty.
func JSON(ctx context.Context, w io.Writer, t *rdmatopo.Topo) error {
	reader := t.VPDReader
	if reader == nil {
		reader = defaultReader
	}
	out := make([]jsonComplex, 0, len(t.Complexes))
	for _, c := range t.Complexes {
		jc := jsonComplex{
			RDMANICPFBDF:  c.CXPF.Address,
			RDMADMABDF:    c.CXDMA.Address,
			GPUBDF:        c.NVGPU.Address,
			Subsystems:    map[string]map[string][]string{},
			NVCXComplexID: c.ID(),
		}
		if name := vpdIdentifier(ctx, reader, c.CXPF); name != "" {
			jc.RDMANICVPDName = name
		}
		if c.CXPF.Node != nil {
			n := c.CXPF.Node.ID
			jc.NUMANode = &n
		}
		if len(c.NVMes) > 0 {
			jc.NVMeBDF = c.NVMes[0].Address
		}

		// Subsystems sorted by device address for stable output.
		devs := make([]*pci.Device, 0, len(c.CXPFs)+2+len(c.NVMes))
		devs = append(devs, c.CXDMA, c.NVGPU)
		devs = append(devs, c.CXPFs...)
		devs = append(devs, c.NVMes...)
		sort.Slice(devs, func(i, j int) bool { return devs[i].Address < devs[j].Address })
		for _, d := range devs {
			subs := pci.DeviceNamesByLinuxSystem(ctx, d)
			if len(subs) == 0 {
				continue
			}
			jc.Subsystems[d.Address] = subs
		}

		out = append(out, jc)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "    ")
	return enc.Encode(out)
}

// Human writes the topology in the same human-readable format as
// the Python implementation's cmd_topology (rdma_topo:495).
func Human(ctx context.Context, w io.Writer, t *rdmatopo.Topo) error {
	reader := t.VPDReader
	if reader == nil {
		reader = defaultReader
	}
	for _, c := range t.Complexes {
		if _, err := fmt.Fprintf(w,
			"RDMA NIC=%s, GPU=%s, RDMA DMA Function=%s\n",
			c.CXPF.Address, c.NVGPU.Address, c.CXDMA.Address,
		); err != nil {
			return err
		}
		if name := vpdIdentifier(ctx, reader, c.CXPF); name != "" {
			fmt.Fprintf(w, "\t%s\n", name)
		}
		if c.CXPF.Node != nil {
			fmt.Fprintf(w, "\tNUMA Node: %d\n", c.CXPF.Node.ID)
		}
		var nics []string
		for _, pf := range c.CXPFs {
			nics = append(nics, pf.Address)
		}
		printList(w, "NIC PCI device", nics)

		subs := c.Subsystems(ctx)
		printList(w, "RDMA device", subs["infiniband"])
		printList(w, "Net device", subs["net"])
		printList(w, "DRM device", subs["drm"])
		printList(w, "NVMe device", subs["nvme"])
	}
	return nil
}

// printList mirrors the Python print_list helper at rdma_topo:444.
// Skips empty input; pluralizes the title when >1 entry.
func printList(w io.Writer, title string, items []string) {
	if len(items) == 0 {
		return
	}
	if len(items) > 1 {
		title = title + "s"
	}
	sorted := append([]string{}, items...)
	sort.Strings(sorted)
	fmt.Fprintf(w, "\t%s: %s\n", title, strings.Join(sorted, ", "))
}

// vpdIdentifier returns the VPD Identifier string (the "Product
// Name" in lspci -vv output) for the device, or empty if VPD
// parsing fails. Errors are swallowed because identifier
// availability is best-effort cosmetic data.
func vpdIdentifier(ctx context.Context, reader rdmatopo.VPDReader, d *pci.Device) string {
	v, err := reader(ctx, d)
	if err != nil || v == nil {
		return ""
	}
	return v.Identifier
}

// defaultReader resolves VPD by calling Device.VPD(ctx) directly.
// Used when a Topo carries no captured reader.
func defaultReader(ctx context.Context, d *pci.Device) (*pci.VPD, error) {
	return d.VPD(ctx)
}
