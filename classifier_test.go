// Use and distribution licensed under the Apache license version 2.

package rdmatopo

import (
	"testing"

	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
)

// makeDev constructs a *pci.Device with the minimum fields populated
// for classifier testing. ghw stores IDs as lowercase 4-char hex
// strings (2 chars for class/subclass/progif), matching pcidb.
func makeDev(vendor, product, class, subclass, progif string) *pci.Device {
	return &pci.Device{
		Vendor:               &pcidb.Vendor{ID: vendor},
		Product:              &pcidb.Product{ID: product},
		Class:                &pcidb.Class{ID: class},
		Subclass:             &pcidb.Subclass{ID: subclass},
		ProgrammingInterface: &pcidb.ProgrammingInterface{ID: progif},
	}
}

func TestClassifyKnownTypes(t *testing.T) {
	cases := []struct {
		name string
		dev  *pci.Device
		want DeviceType
	}{
		{
			"Grace root port 0x22B1",
			makeDev("10de", "22b1", "06", "04", "00"),
			TypeGraceRP,
		},
		{
			"Grace root port 0x22B2",
			makeDev("10de", "22b2", "06", "04", "00"),
			TypeGraceRP,
		},
		{
			"Grace root port 0x22B8",
			makeDev("10de", "22b8", "06", "04", "00"),
			TypeGraceRP,
		},
		{
			"ConnectX-7 NIC",
			makeDev("15b3", "1021", "02", "00", "00"),
			TypeCXNIC,
		},
		{
			"ConnectX-8 NIC",
			makeDev("15b3", "1023", "02", "00", "00"),
			TypeCXNIC,
		},
		{
			"BlueField-3 NIC",
			makeDev("15b3", "a2dc", "02", "00", "00"),
			TypeBF3NIC,
		},
		{
			"ConnectX-8 DMA Direct",
			makeDev("15b3", "2100", "08", "80", "00"),
			TypeCXDMA,
		},
		{
			"CX switch",
			makeDev("15b3", "197c", "06", "04", "00"),
			TypeCXSwitch,
		},
		{
			"BF3 switch",
			makeDev("15b3", "197b", "06", "04", "00"),
			TypeBF3Switch,
		},
		{
			"NVMe device by class",
			makeDev("144d", "a804", "01", "08", "02"),
			TypeNVMe,
		},
		{
			"NVIDIA GPU (Blackwell) by class",
			makeDev("10de", "2330", "03", "02", "00"),
			TypeNVGPU,
		},
		{
			"NVIDIA GPU (any subclass under 03)",
			makeDev("10de", "20b0", "03", "00", "00"),
			TypeNVGPU,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.dev)
			if got != tc.want {
				t.Errorf("Classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyUnknownReturnsEmpty(t *testing.T) {
	cases := []*pci.Device{
		// Intel NIC (known vendor, not in our table).
		makeDev("8086", "1572", "02", "00", "00"),
		// AMD GPU (class 03, wrong vendor).
		makeDev("1002", "73bf", "03", "00", "00"),
		// SATA controller (class 01, wrong subclass/progif for NVMe).
		makeDev("8086", "9d03", "01", "06", "01"),
	}
	for i, dev := range cases {
		got := Classify(dev)
		if got != TypeUnknown {
			t.Errorf("case %d: Classify() = %q, want TypeUnknown", i, got)
		}
	}
}

func TestClassifyHandlesNilFields(t *testing.T) {
	// Devices with nil pcidb pointers must not panic. The GPU rule
	// only inspects Vendor and Class, so a vendor+class-only NVIDIA
	// device still classifies as NVGPU; everything else falls
	// through to TypeUnknown.
	cases := []struct {
		name string
		dev  *pci.Device
		want DeviceType
	}{
		{"all nil", &pci.Device{}, TypeUnknown},
		{"vendor only", &pci.Device{Vendor: &pcidb.Vendor{ID: "10de"}}, TypeUnknown},
		{
			"NVIDIA vendor + class 03, no product",
			&pci.Device{Vendor: &pcidb.Vendor{ID: "10de"}, Class: &pcidb.Class{ID: "03"}},
			TypeNVGPU,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.dev); got != tc.want {
				t.Errorf("Classify() = %q, want %q", got, tc.want)
			}
		})
	}
}
