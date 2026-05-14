// Use and distribution licensed under the Apache license version 2.

package report

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jaypipes/ghw/pkg/topology"

	"github.com/dims/rdma_topo/internal/topotest"
)

func TestJSONMinimalShape(t *testing.T) {
	topo := topotest.BuildSampleTopo(t, false, false, false)
	var buf bytes.Buffer
	if err := JSON(context.Background(), &buf, topo); err != nil {
		t.Fatalf("JSON returned err: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	c := got[0]
	if c["rdma_nic_pf_bdf"] != "0000:07:00.0" {
		t.Errorf("rdma_nic_pf_bdf = %v", c["rdma_nic_pf_bdf"])
	}
	if c["rdma_dma_bdf"] != "0000:03:00.0" {
		t.Errorf("rdma_dma_bdf = %v", c["rdma_dma_bdf"])
	}
	if c["gpu_bdf"] != "0000:06:00.0" {
		t.Errorf("gpu_bdf = %v", c["gpu_bdf"])
	}
	// Optional fields must be omitted when not present.
	if _, ok := c["rdma_nic_vpd_name"]; ok {
		t.Errorf("rdma_nic_vpd_name should be omitted, got %v", c["rdma_nic_vpd_name"])
	}
	if _, ok := c["numa_node"]; ok {
		t.Errorf("numa_node should be omitted, got %v", c["numa_node"])
	}
	if _, ok := c["nvme_bdf"]; ok {
		t.Errorf("nvme_bdf should be omitted, got %v", c["nvme_bdf"])
	}
}

func TestJSONOptionalFields(t *testing.T) {
	topo := topotest.BuildSampleTopo(t, true /*vpdName*/, true /*numa*/, true /*nvme*/)
	var buf bytes.Buffer
	if err := JSON(context.Background(), &buf, topo); err != nil {
		t.Fatalf("JSON returned err: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	c := got[0]
	if c["rdma_nic_vpd_name"] != "ConnectX-8 NIC sample" {
		t.Errorf("rdma_nic_vpd_name = %v", c["rdma_nic_vpd_name"])
	}
	if n, ok := c["numa_node"].(float64); !ok || int(n) != 3 {
		t.Errorf("numa_node = %v (want 3)", c["numa_node"])
	}
	if c["nvme_bdf"] != "0000:0a:00.0" {
		t.Errorf("nvme_bdf = %v", c["nvme_bdf"])
	}
}

// TestJSONMatchesGoldenFixture pins the JSON output shape against
// testdata/topo.golden.json. Drift here is the signal that
// downstream consumers (the DRA drivers, scripts) would also see.
// When the schema needs to change, update the golden file and the
// JSON doc comment in the same commit.
func TestJSONMatchesGoldenFixture(t *testing.T) {
	topo := topotest.BuildSampleTopo(t, true, true, true)
	var buf bytes.Buffer
	if err := JSON(context.Background(), &buf, topo); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	want, err := os.ReadFile("testdata/topo.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("JSON output drift\n--- got ---\n%s--- want ---\n%s", buf.String(), string(want))
	}
}

// TestHumanNUMABoundaries locks in the rule that the NUMA line
// tracks the CXPF's Node pointer: emit when set (including the
// zero ID), elide when nil. ghw currently maps a sysfs value of -1
// to nil so the negative case only matters if a future ghw change
// starts surfacing it.
func TestHumanNUMABoundaries(t *testing.T) {
	cases := []struct {
		name    string
		node    *topology.Node
		want    string
		notWant string
	}{
		{name: "nil", node: nil, notWant: "NUMA Node:"},
		{name: "zero", node: &topology.Node{ID: 0}, want: "NUMA Node: 0"},
		{name: "positive", node: &topology.Node{ID: 7}, want: "NUMA Node: 7"},
		{name: "negative", node: &topology.Node{ID: -1}, want: "NUMA Node: -1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topo := topotest.BuildSampleTopo(t, false, false, false)
			topo.Complexes[0].CXPF.Node = tc.node
			var buf bytes.Buffer
			if err := Human(context.Background(), &buf, topo); err != nil {
				t.Fatalf("Human: %v", err)
			}
			got := buf.String()
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("missing %q in:\n%s", tc.want, got)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("unexpected %q in:\n%s", tc.notWant, got)
			}
		})
	}
}

func TestHumanContains(t *testing.T) {
	topo := topotest.BuildSampleTopo(t, true, true, true)
	var buf bytes.Buffer
	if err := Human(context.Background(), &buf, topo); err != nil {
		t.Fatalf("Human returned err: %v", err)
	}
	out := buf.String()
	mustContain := []string{
		"RDMA NIC=0000:07:00.0, GPU=0000:06:00.0, RDMA DMA Function=0000:03:00.0",
		"ConnectX-8 NIC sample",
		"NUMA Node: 3",
		"NIC PCI device: 0000:07:00.0",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nfull output:\n%s", s, out)
		}
	}
}

func TestHumanPluralizesTitles(t *testing.T) {
	var buf bytes.Buffer
	printList(&buf, "Net device", []string{"eth0"})
	if got := buf.String(); !strings.Contains(got, "Net device:") || strings.Contains(got, "Net devices:") {
		t.Errorf("singular: got %q", got)
	}

	buf.Reset()
	printList(&buf, "Net device", []string{"eth0", "eth1"})
	if got := buf.String(); !strings.Contains(got, "Net devices:") {
		t.Errorf("plural: got %q", got)
	}
}

func TestHumanSkipsEmptyList(t *testing.T) {
	var buf bytes.Buffer
	printList(&buf, "RDMA device", nil)
	if got := buf.String(); got != "" {
		t.Errorf("empty input should produce no output, got %q", got)
	}
}
