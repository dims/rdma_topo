// Use and distribution licensed under the Apache license version 2.

//go:build linux

// Package integration exercises the rdmatopo library end-to-end
// against a fake sysfs layout built in a temp dir. It lives in its
// own directory so the slow filesystem fixtures stay out of the
// main unit-test packages.
package integration

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/pci"

	rdmatopo "github.com/dims/rdma_topo"
)

// fakeSysfs builds a synthetic /sys layout in t.TempDir() suitable
// for feeding to ghw via option.WithChroot. PCI devices are linked
// via the same realpath/symlink convention real sysfs uses.
type fakeSysfs struct {
	t        *testing.T
	root     string
	devsDir  string            // /sys/bus/pci/devices
	realpath map[string]string // BDF -> absolute realpath dir
}

func newFakeSysfs(t *testing.T) *fakeSysfs {
	t.Helper()
	root := t.TempDir()
	devsDir := filepath.Join(root, "sys", "bus", "pci", "devices")
	if err := os.MkdirAll(devsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// /sys/devices/pci0000:00 is the conventional segment root.
	if err := os.MkdirAll(filepath.Join(root, "sys", "devices", "pci0000:00"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeSysfs{
		t:        t,
		root:     root,
		devsDir:  devsDir,
		realpath: map[string]string{},
	}
}

// addDev creates a device entry at the given BDF, with the supplied
// vendor/product/class/subclass/progif quintuple, parented under
// parentBDF ("" for a segment-root device).
func (f *fakeSysfs) addDev(addr, parentBDF, vendor, product, class, subclass, progif string) string {
	f.t.Helper()
	var realDir string
	if parentBDF == "" {
		realDir = filepath.Join(f.root, "sys", "devices", "pci0000:00", addr)
	} else {
		parentReal, ok := f.realpath[parentBDF]
		if !ok {
			f.t.Fatalf("parent BDF %q not registered before child %q", parentBDF, addr)
		}
		realDir = filepath.Join(parentReal, addr)
	}
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	modalias := fmt.Sprintf("pci:v0000%sd0000%ssv00000000sd00000000bc%ssc%si%s",
		upHex4(vendor), upHex4(product),
		upHex2(class), upHex2(subclass), upHex2(progif),
	)
	if err := os.WriteFile(filepath.Join(realDir, "modalias"), []byte(modalias+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	// revision is required by ghw's TestPCI flow but not by our
	// classifier; still, write a value so the field is populated.
	_ = os.WriteFile(filepath.Join(realDir, "revision"), []byte("0x00\n"), 0o644)
	// Symlink /sys/bus/pci/devices/<addr> -> ../../../devices/pci0000:00/.../addr
	rel, err := filepath.Rel(f.devsDir, realDir)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(f.devsDir, addr)); err != nil {
		f.t.Fatal(err)
	}
	f.realpath[addr] = realDir
	return realDir
}

// addVPD writes a synthetic VPD binary for the device at addr.
func (f *fakeSysfs) addVPD(addr string, identifier string, keywords map[string]string) {
	f.t.Helper()
	real, ok := f.realpath[addr]
	if !ok {
		f.t.Fatalf("device %q not registered", addr)
	}
	var data []byte
	appendLarge := func(tag byte, body []byte) {
		data = append(data, tag)
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(body)))
		data = append(data, l[:]...)
		data = append(data, body...)
	}
	if identifier != "" {
		appendLarge(0x82, []byte(identifier))
	}
	if len(keywords) > 0 {
		var ro []byte
		for k, v := range keywords {
			if len(k) != 2 {
				f.t.Fatalf("vpd keyword %q must be 2 chars", k)
			}
			ro = append(ro, k[0], k[1], byte(len(v)))
			ro = append(ro, []byte(v)...)
		}
		appendLarge(0x90, ro)
	}
	data = append(data, 0x78) // end tag
	if err := os.WriteFile(filepath.Join(real, "vpd"), data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func upHex4(s string) string { return fmt.Sprintf("%04X", mustHex(s)) }
func upHex2(s string) string { return fmt.Sprintf("%02X", mustHex(s)) }

func mustHex(s string) uint64 {
	var v uint64
	for _, ch := range s {
		v <<= 4
		switch {
		case ch >= '0' && ch <= '9':
			v |= uint64(ch - '0')
		case ch >= 'a' && ch <= 'f':
			v |= uint64(ch - 'a' + 10)
		case ch >= 'A' && ch <= 'F':
			v |= uint64(ch - 'A' + 10)
		}
	}
	return v
}

func TestIntegrationFakeSysfsHappyPath(t *testing.T) {
	f := newFakeSysfs(t)

	// Mirror buildSampleComplex's topology against a real sysfs layout.
	f.addDev("0000:00:00.0", "", "10de", "22b1", "06", "04", "00")             // grace_rp
	f.addDev("0000:01:00.0", "0000:00:00.0", "15b3", "197c", "06", "04", "00") // cx_usp
	f.addDev("0000:02:00.0", "0000:01:00.0", "15b3", "197c", "06", "04", "00") // cx_dma_dsp
	f.addDev("0000:03:00.0", "0000:02:00.0", "15b3", "2100", "08", "80", "00") // cx_dma
	f.addDev("0000:02:01.0", "0000:01:00.0", "15b3", "197c", "06", "04", "00") // nvgpu_dsp1
	f.addDev("0000:04:00.0", "0000:02:01.0", "15b3", "197c", "06", "04", "00") // nvgpu_usp2
	f.addDev("0000:05:00.0", "0000:04:00.0", "15b3", "197c", "06", "04", "00") // nvgpu_dsp2
	f.addDev("0000:06:00.0", "0000:05:00.0", "10de", "2330", "03", "02", "00") // nvgpu
	f.addDev("0000:07:00.0", "", "15b3", "1023", "02", "00", "00")             // cx_pf (independent root)

	// VPD V3 pairing: write bytes into the fake sysfs and let
	// Device.VPD(ctx) read them through the chroot-aware ctx.
	f.addVPD("0000:03:00.0", "CX-DMA ident", map[string]string{"V3": "real-uuid"})
	f.addVPD("0000:07:00.0", "ConnectX-8 NIC", map[string]string{"V3": "real-uuid"})

	ctx := ghw.WithChroot(f.root)(context.Background())
	ctx = ghw.WithDisableTopology()(ctx)
	info, err := pci.New(ctx)
	if err != nil {
		t.Fatalf("pci.New returned err: %v", err)
	}

	topo, err := rdmatopo.BuildTopology(ctx, info.Devices)
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
	if c.CXPF.Address != "0000:07:00.0" {
		t.Errorf("CXPF = %q, want 0000:07:00.0", c.CXPF.Address)
	}
	if c.CXDMA.Address != "0000:03:00.0" {
		t.Errorf("CXDMA = %q, want 0000:03:00.0", c.CXDMA.Address)
	}
	if c.NVGPU.Address != "0000:06:00.0" {
		t.Errorf("NVGPU = %q, want 0000:06:00.0", c.NVGPU.Address)
	}
	vpd, err := c.CXPF.VPD(ctx)
	if err != nil {
		t.Fatalf("CXPF VPD: %v", err)
	}
	if vpd.Identifier != "ConnectX-8 NIC" {
		t.Errorf("CXPF VPD identifier = %q, want %q", vpd.Identifier, "ConnectX-8 NIC")
	}
}

func TestIntegrationFakeSysfsNoDMA(t *testing.T) {
	f := newFakeSysfs(t)
	f.addDev("0000:00:00.0", "", "8086", "1572", "02", "00", "00") // Intel NIC, no cx_dma
	ctx := ghw.WithChroot(f.root)(context.Background())
	ctx = ghw.WithDisableTopology()(ctx)
	info, err := pci.New(ctx)
	if err != nil {
		t.Fatalf("pci.New returned err: %v", err)
	}
	_, err = rdmatopo.BuildTopology(ctx, info.Devices)
	if err == nil || err.Error() != "no ConnectX DMA Direct functions detected" {
		t.Errorf("got err %v, want ErrNoCXDMA", err)
	}
}
