package libvirt

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	libvirtdriver "github.com/code-ready/machine/drivers/libvirt"
	"github.com/code-ready/machine/libmachine/drivers"
	"github.com/code-ready/machine/libmachine/log"
	"github.com/code-ready/machine/libmachine/mcnflag"
	"github.com/code-ready/machine/libmachine/mcnutils"
	"github.com/code-ready/machine/libmachine/state"
	libvirtxml "github.com/libvirt/libvirt-go-xml"
)

type Driver struct {
	*libvirtdriver.Driver
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.IntFlag{
			Name:  "crc-libvirt-memory",
			Usage: "Size of memory for host in MB",
			Value: DefaultMemory,
		},
		mcnflag.IntFlag{
			Name:  "crc-libvirt-cpu-count",
			Usage: "Number of CPUs",
			Value: DefaultCPUs,
		},
		mcnflag.StringFlag{
			Name:  "crc-libvirt-network",
			Usage: "Name of network to connect to",
			Value: DefaultNetwork,
		},
		mcnflag.StringFlag{
			Name:  "crc-libvirt-cachemode",
			Usage: "Disk cache mode: default, none, writethrough, writeback, directsync, or unsafe",
			Value: DefaultCacheMode,
		},
		mcnflag.StringFlag{
			Name:  "crc-libvirt-iomode",
			Usage: "Disk IO mode: threads, native",
			Value: DefaultIOMode,
		},
		mcnflag.StringFlag{
			EnvVar: "CRC_LIBVIRT_SSHUSER",
			Name:   "crc-libvirt-sshuser",
			Usage:  "SSH username",
			Value:  DefaultSSHUser,
		},
	}
}

type DomainConfig struct {
	DomainName   string
	Memory       int
	CPU          int
	CacheMode    string
	IOMode       string
	DiskPath     string
	ExtraDevices []string
}

func (d *Driver) GetMachineName() string {
	return d.MachineName
}

func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

func (d *Driver) GetSSHKeyPath() string {
	return d.SSHKeyPath
}

func (d *Driver) GetSSHPort() (int, error) {
	if d.SSHPort == 0 {
		d.SSHPort = DefaultSSHPort
	}

	return d.SSHPort, nil
}

func (d *Driver) GetSSHUsername() string {
	if d.SSHUser == "" {
		d.SSHUser = DefaultSSHUser
	}

	return d.SSHUser
}

func (d *Driver) DriverName() string {
	return DriverName
}

func (d *Driver) DriverVersion() string {
	return DriverVersion
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	log.Debugf("SetConfigFromFlags called")
	d.Memory = flags.Int("libvirt-memory")
	d.CPU = flags.Int("libvirt-cpu-count")
	d.Network = flags.String("libvirt-network")
	d.CacheMode = flags.String("libvirt-cachemode")
	d.IOMode = flags.String("libvirt-iomode")
	d.SSHPort = 22

	// CRC system bundle
	d.BundleName = flags.String("libvirt-bundlepath")
	return nil
}

func convertMBToKiB(sizeMb int) uint64 {
	return uint64(sizeMb) * 1000 * 1000 / 1024
}

func (d *Driver) setMemory(memorySize int) error {
	log.Debugf("Setting memory to %d MB", memorySize)
	/* d.Memory is in MB, SetMemoryFlags expects kiB */
	if _, err := execute(virsh("setmaxmem", "--config", d.MachineName, fmt.Sprintf("%v", convertMBToKiB(memorySize))), nil); err != nil {
		return err
	}
	if _, err := execute(virsh("setmem", "--config", d.MachineName, fmt.Sprintf("%v", convertMBToKiB(memorySize))), nil); err != nil {
		return err
	}
	d.Memory = memorySize
	return nil
}

func (d *Driver) setVcpus(cpus uint) error {
	log.Debugf("Setting vcpus to %d", cpus)
	if _, err := execute(virsh("setvcpus", "--maximum", "--config", d.MachineName, strconv.Itoa(int(cpus))), nil); err != nil {
		return err
	}
	if _, err := execute(virsh("setvcpus", "--config", d.MachineName, strconv.Itoa(int(cpus))), nil); err != nil {
		return err
	}
	d.CPU = int(cpus)
	return nil
}

func (d *Driver) UpdateConfigRaw(rawConfig []byte) error {
	var newDriver libvirtdriver.Driver
	err := json.Unmarshal(rawConfig, &newDriver)
	if err != nil {
		return err
	}
	// FIXME: not clear what the upper layers should do in case of partial errors?
	// is it the drivers implementation responsibility to keep a consistent internal state,
	// and should it return its (partial) new state when an error occurred?
	if newDriver.Memory != d.Memory {
		err := d.setMemory(newDriver.Memory)
		if err != nil {
			log.Warnf("Failed to update memory: %v", err)
			return err
		}
	}
	if newDriver.CPU != d.CPU {
		err := d.setVcpus(uint(newDriver.CPU))
		if err != nil {
			log.Warnf("Failed to update CPU count: %v", err)
			return err
		}
	}
	if newDriver.SSHKeyPath != d.SSHKeyPath {
		log.Debugf("Updating SSH Key Path", d.SSHKeyPath, newDriver.SSHKeyPath)
	}

	*d.Driver = newDriver
	return nil
}

func (d *Driver) GetURL() (string, error) {
	return "", nil
}

// Create, or verify the private network is properly configured
func (d *Driver) validateNetwork() error {
	if d.Network == "" {
		return nil
	}
	log.Debug("Validating network")
	xmldoc, err := execute(virsh("net-dumpxml", d.Network), nil)
	if err != nil {
		return err
	}
	var nw libvirtxml.Network
	if err := xml.Unmarshal([]byte(xmldoc), &nw); err != nil {
		return err
	}

	if len(nw.IPs) != 1 {
		return fmt.Errorf("unexpected number of IPs for network %s", d.Network)
	}
	if nw.IPs[0].Address == "" {
		return fmt.Errorf("%s network doesn't have DHCP configured", d.Network)
	}
	// Corner case, but might happen...
	out, err := execute(virsh("net-info", d.Network), nil)
	if err != nil {
		return err
	}
	active, err := regexp.MatchString(`Active:\s*yes`, out)
	if err != nil {
		return err
	}
	if !active {
		log.Debugf("Reactivating network")
		if _, err := execute(virsh("net-start", d.Network), nil); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) PreCreateCheck() error {
	err := d.validateNetwork()
	if err != nil {
		return err
	}
	return nil
}

func (d *Driver) getDiskPath() string {
	return d.ResolveStorePath(fmt.Sprintf("%s.%s", d.MachineName, d.ImageFormat))
}

func (d *Driver) Create() error {
	if d.ImageFormat != "qcow2" {
		return fmt.Errorf("Unsupported VM image format: %s", d.ImageFormat)
	}
	if err := createImage(d.ImageSourcePath, d.getDiskPath()); err != nil {
		return err
	}

	if err := os.MkdirAll(d.ResolveStorePath("."), 0755); err != nil {
		return err
	}

	// Libvirt typically runs as a deprivileged service account and
	// needs the execute bit set for directories that contain disks
	for dir := d.ResolveStorePath("."); dir != "/"; dir = filepath.Dir(dir) {
		log.Debugf("Verifying executable bit set on %s", dir)
		info, err := os.Stat(dir)
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&0001 != 1 {
			log.Debugf("Setting executable bit set on %s", dir)
			mode |= 0001
			if err := os.Chmod(dir, mode); err != nil {
				return err
			}
		}
	}

	log.Debugf("Defining VM...")
	xml, err := domainXML(d)
	if err != nil {
		return err
	}

	cmd := virsh("define", "/dev/stdin")
	cmd.Stdin = bytes.NewReader([]byte(xml))

	if _, err := execute(cmd, nil); err != nil {
		log.Warnf("Failed to create the VM: %s", err)
		return err
	}
	log.Debugf("Adding the file: %s", filepath.Join(d.ResolveStorePath("."), fmt.Sprintf(".%s-exist", d.MachineName)))
	_, _ = os.OpenFile(filepath.Join(d.ResolveStorePath("."), fmt.Sprintf(".%s-exist", d.MachineName)), os.O_RDONLY|os.O_CREATE, 0666)

	return d.Start()
}

func createImage(src, dst string) error {
	start := time.Now()
	defer func() {
		log.Debugf("image creation took %s", time.Since(start).String())
	}()
	// #nosec G204
	cmd := exec.Command("qemu-img",
		"create",
		"-f", "qcow2",
		"-o", fmt.Sprintf("backing_file=%s", src),
		dst)
	if err := cmd.Run(); err != nil {
		log.Debugf("qemu-img create failed, falling back to copy: %v", err)
		return mcnutils.CopyFile(src, dst)
	}
	return nil
}

func (d *Driver) Start() error {
	log.Debugf("Starting VM %s", d.MachineName)
	if _, err := execute(virsh("start", d.MachineName), nil); err != nil {
		log.Warnf("Failed to start: %s", err)
		return err
	}

	if d.Network == "" {
		return nil
	}

	// They wont start immediately
	time.Sleep(5 * time.Second)

	for i := 0; i < 60; i++ {
		ip, err := d.GetIP()
		if err != nil {
			return fmt.Errorf("%v: getting ip during machine start", err)
		}

		if ip == "" {
			log.Debugf("Waiting for machine to come up %d/%d", i, 60)
			time.Sleep(3 * time.Second)
			continue
		}

		if ip != "" {
			log.Infof("Found IP for machine: %s", ip)
			d.IPAddress = ip
			break
		}
		log.Debugf("Waiting for the VM to come up... %d", i)
	}

	if d.IPAddress == "" {
		log.Warnf("Unable to determine VM's IP address, did it fail to boot?")
	}
	return nil
}

func (d *Driver) Stop() error {
	log.Debugf("Stopping VM %s", d.MachineName)
	s, err := d.GetState()
	if err != nil {
		return err
	}

	if s != state.Stopped {
		_, err := execute(virsh("shutdown", d.MachineName), nil)
		if err != nil {
			log.Warnf("Failed to gracefully shutdown VM")
			return err
		}
		for i := 0; i < 120; i++ {
			time.Sleep(time.Second)
			s, _ := d.GetState()
			log.Debugf("VM state: %s", s)
			if s == state.Stopped {
				return nil
			}
		}
		return errors.New("VM Failed to gracefully shutdown, try the kill command")
	}
	return nil
}

func (d *Driver) Remove() error {
	log.Debugf("Removing VM %s", d.MachineName)
	_, _ = execute(virsh("destroy", d.MachineName), nil)
	_, err := execute(virsh("undefine", d.MachineName), nil)
	return err
}

func (d *Driver) Restart() error {
	log.Debugf("Restarting VM %s", d.MachineName)
	if err := d.Stop(); err != nil {
		return err
	}
	return d.Start()
}

func (d *Driver) Kill() error {
	log.Debugf("Killing VM %s", d.MachineName)
	_, err := execute(virsh("destroy", d.MachineName), nil)
	return err
}

func (d *Driver) GetState() (state.State, error) {
	log.Debugf("Getting current state...")
	out, err := execute(virsh("domstate", d.MachineName), nil)
	if err != nil {
		return state.None, err
	}
	switch strings.TrimSpace(string(out)) {
	case "running":
		return state.Running, nil
	case "idle":
		return state.Error, nil
	case "in shutdown":
		return state.Running, nil
	case "shut off":
		return state.Stopped, nil
	case "crashed":
		return state.Error, nil
	case "pmsuspended":
		return state.Error, nil
	}
	return state.None, nil
}

// This implementation is specific to default networking in libvirt
// with dnsmasq
func (d *Driver) getMAC() (string, error) {
	out, err := execute(virsh("dumpxml", d.MachineName), nil)
	if err != nil {
		return "", err
	}
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(out), &dom); err != nil {
		return "", err
	}
	return dom.Devices.Interfaces[0].MAC.Address, nil
}

func (d *Driver) getIPByMacFromSettings(mac string) (string, error) {
	xmldoc, err := execute(virsh("net-dumpxml", d.Network), nil)
	if err != nil {
		return "", err
	}
	var nw libvirtxml.Network
	if err := xml.Unmarshal([]byte(xmldoc), &nw); err != nil {
		return "", err
	}
	if len(nw.IPs) != 1 {
		return "", fmt.Errorf("unexpected number of IPs for network %s", d.Network)
	}
	for _, host := range nw.IPs[0].DHCP.Hosts {
		if strings.EqualFold(host.MAC, mac) {
			log.Debugf("IP address: %s", host.IP)
			return host.IP, nil
		}
	}
	return "", nil
}

func (d *Driver) GetIP() (string, error) {
	log.Debugf("GetIP called for %s", d.MachineName)
	s, err := d.GetState()
	if err != nil {
		return "", fmt.Errorf("%v : machine in unknown state", err)
	}
	if s != state.Running {
		return "", errors.New("host is not running")
	}
	mac, err := d.getMAC()
	if err != nil {
		return "", err
	}

	return d.getIPByMacFromSettings(mac)
}

func NewDriver(hostName, storePath string) drivers.Driver {
	return &Driver{
		Driver: &libvirtdriver.Driver{
			VMDriver: &drivers.VMDriver{
				BaseDriver: &drivers.BaseDriver{
					MachineName: hostName,
					StorePath:   storePath,
					SSHUser:     DefaultSSHUser,
				},
			},
			Network: DefaultNetwork,
		},
	}
}
