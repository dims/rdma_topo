// Use and distribution licensed under the Apache license version 2.

package rdmatopo

import "github.com/jaypipes/ghw/pkg/pci"

// DeviceType is the classification assigned to a PCI device by the
// rdma_topo classifier. An empty string means the device is not of
// interest to the NVCX topology matcher.
type DeviceType string

const (
	TypeUnknown   DeviceType = ""
	TypeGraceRP   DeviceType = "grace_rp"   // NVIDIA Grace PCI Root Port Bridge
	TypeCXNIC     DeviceType = "cx_nic"     // ConnectX-7/8 NIC PF
	TypeBF3NIC    DeviceType = "bf3_nic"    // BlueField-3 NIC PF
	TypeCXDMA     DeviceType = "cx_dma"     // ConnectX-8 DMA Direct function
	TypeBF3Switch DeviceType = "bf3_switch" // USP/DSP of a BlueField-3 switch
	TypeCXSwitch  DeviceType = "cx_switch"  // USP/DSP of a ConnectX switch
	TypeNVMe      DeviceType = "nvme"       // NVMe storage controller (class 010802)
	TypeNVGPU     DeviceType = "nvgpu"      // NVIDIA GPU (class 03)
)

// PCI vendor IDs we care about, lowercase hex to match pcidb.
const (
	vendorNVIDIA   = "10de"
	vendorMellanox = "15b3"
)

// matcher is one row of the classification table. Predicates run in
// table order; the first match wins (mirroring Python's dict iteration
// order in rdma_topo.PCITopo).
type matcher struct {
	devType DeviceType
	match   func(*pci.Device) bool
}

// classificationTable mirrors rdma_topo's pci_device_types dict at
// rdma_topo:64-76. Keep this list in sync with upstream when new
// part numbers are added.
var classificationTable = []matcher{
	// Grace PCI root port bridges
	vendorDeviceMatcher(TypeGraceRP, vendorNVIDIA, "22b1"),
	vendorDeviceMatcher(TypeGraceRP, vendorNVIDIA, "22b2"),
	vendorDeviceMatcher(TypeGraceRP, vendorNVIDIA, "22b8"),

	// ConnectX-7 / ConnectX-8 NIC PFs
	vendorDeviceMatcher(TypeCXNIC, vendorMellanox, "1021"),
	vendorDeviceMatcher(TypeCXNIC, vendorMellanox, "1023"),

	// BlueField-3 NIC PF
	vendorDeviceMatcher(TypeBF3NIC, vendorMellanox, "a2dc"),

	// ConnectX-8 DMA Direct function
	vendorDeviceMatcher(TypeCXDMA, vendorMellanox, "2100"),

	// BF3 / CX switch USP+DSP
	vendorDeviceMatcher(TypeBF3Switch, vendorMellanox, "197b"),
	vendorDeviceMatcher(TypeCXSwitch, vendorMellanox, "197c"),

	// NVMe by PCI class 010802 (storage / non-volatile memory / NVMe)
	classMatcher(TypeNVMe, "01", "08", "02"),

	// NVIDIA GPUs: any NVIDIA device with class 0x03 (display
	// controllers). Subclass and programming interface are ignored
	// per rdma_topo:56-60.
	{
		devType: TypeNVGPU,
		match: func(d *pci.Device) bool {
			if d.Vendor == nil || d.Class == nil {
				return false
			}
			return d.Vendor.ID == vendorNVIDIA && d.Class.ID == "03"
		},
	},
}

// vendorDeviceMatcher builds a matcher that compares the device's
// vendor and product IDs (both 4-char lowercase hex strings).
func vendorDeviceMatcher(devType DeviceType, vendorID, productID string) matcher {
	return matcher{
		devType: devType,
		match: func(d *pci.Device) bool {
			if d.Vendor == nil || d.Product == nil {
				return false
			}
			return d.Vendor.ID == vendorID && d.Product.ID == productID
		},
	}
}

// classMatcher builds a matcher that compares the device's PCI class,
// subclass and programming-interface bytes (each 2-char lowercase
// hex). It mirrors rdma_topo.PCI_DEVICE_CLASS at rdma_topo:48-53.
func classMatcher(devType DeviceType, classID, subclassID, progIfID string) matcher {
	return matcher{
		devType: devType,
		match: func(d *pci.Device) bool {
			if d.Class == nil || d.Subclass == nil || d.ProgrammingInterface == nil {
				return false
			}
			return d.Class.ID == classID &&
				d.Subclass.ID == subclassID &&
				d.ProgrammingInterface.ID == progIfID
		},
	}
}

// Classify returns the rdma_topo DeviceType for the given PCI device,
// or TypeUnknown if no matcher applies. The first matcher in
// classificationTable order to match wins.
func Classify(d *pci.Device) DeviceType {
	for _, m := range classificationTable {
		if m.match(d) {
			return m.devType
		}
	}
	return TypeUnknown
}
