package libvirt

var DriverVersion = "dev"

const (
	DriverName = "libvirt"

	connectionString = "qemu:///system"
	DefaultMemory    = 8096
	DefaultCPUs      = 4
	DefaultNetwork   = "crc"
	DefaultPool      = "crc"
	DefaultCacheMode = "default"
	DefaultIOMode    = "threads"
	DefaultSSHUser   = "core"
	DefaultSSHPort   = 22
)
