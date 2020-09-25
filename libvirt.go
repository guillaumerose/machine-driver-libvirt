package libvirt

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	golibvirt "github.com/digitalocean/go-libvirt"

	// Machine-drivers
	libvirtdriver "github.com/code-ready/machine/drivers/libvirt"
	"github.com/code-ready/machine/libmachine/drivers"
	"github.com/code-ready/machine/libmachine/log"
	"github.com/code-ready/machine/libmachine/mcnflag"
	"github.com/code-ready/machine/libmachine/mcnutils"
	"github.com/code-ready/machine/libmachine/state"
)

const filePrefix = "file://"

type Driver struct {
	*libvirtdriver.Driver

	// Libvirt connection and state
	conn     *golibvirt.Libvirt
	VM       *golibvirt.Domain
	vmLoaded bool
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
	d.DiskPath = d.ResolveStorePath(fmt.Sprintf("%s.img", d.MachineName))

	// CRC system bundle
	d.BundleName = flags.String("libvirt-bundlepath")
	return nil
}

func (d *Driver) GetURL() (string, error) {
	return "", nil
}

func (d *Driver) getConn() (*golibvirt.Libvirt, error) {
	if d.conn == nil {
		c, err := net.DialTimeout("unix", "/var/run/libvirt/libvirt-sock", 2*time.Second)
		if err != nil {
			return nil, err
		}

		l := golibvirt.New(c)
		if _, err := l.AuthPolkit(); err != nil {
			return nil, err
		}
		if err := l.Connect(); err != nil {
			return nil, err
		}
		d.conn = l
	}
	return d.conn, nil
}

// Create, or verify the private network is properly configured
func (d *Driver) validateNetwork() error {
	if d.Network == "" {
		return nil
	}
	log.Debug("Validating network")
	conn, err := d.getConn()
	if err != nil {
		return err
	}
	network, err := conn.NetworkLookupByName(d.Network)
	if err != nil {
		return fmt.Errorf("Use 'crc setup' for define the network, %+v", err)
	}
	xmldoc, err := conn.NetworkGetXMLDesc(network, 0)
	if err != nil {
		return err
	}
	/* XML structure:
	<network>
	    ...
	    <ip address='a.b.c.d' prefix='24'>
	        <dhcp>
	            <host mac='' name='' ip=''/>
	        </dhcp>
	*/
	type IP struct {
		Address string `xml:"address,attr"`
		Netmask string `xml:"prefix,attr"`
	}
	type Network struct {
		IP IP `xml:"ip"`
	}

	var nw Network
	err = xml.Unmarshal([]byte(xmldoc), &nw)
	if err != nil {
		return err
	}

	if nw.IP.Address == "" {
		return fmt.Errorf("%s network doesn't have DHCP configured", d.Network)
	}
	// Corner case, but might happen...
	if active, err := conn.NetworkIsActive(network); active == 0 {
		log.Debugf("Reactivating network: %s", err)
		err = conn.NetworkCreate(network)
		if err != nil {
			log.Warnf("Failed to Start network: %s", err)
			return err
		}
	}
	return nil
}

func (d *Driver) PreCreateCheck() error {
	conn, err := d.getConn()
	if err != nil {
		return err
	}

	// TODO We could look at conn.GetCapabilities()
	// parse the XML, and look for kvm
	log.Debug("About to check libvirt version")

	// TODO might want to check minimum version
	_, err = conn.ConnectGetLibVersion()
	if err != nil {
		log.Warnf("Unable to get libvirt version")
		return err
	}
	err = d.validateNetwork()
	if err != nil {
		return err
	}
	// Others...?
	return nil
}

func (d *Driver) Create() error {
	if !strings.HasPrefix(d.DiskPathURL, filePrefix) {
		return fmt.Errorf("DiskPathURL must start with %s", filePrefix)
	}

	if err := createImage(strings.ReplaceAll(d.DiskPathURL, filePrefix, ""), d.DiskPath); err != nil {
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

	conn, err := d.getConn()
	if err != nil {
		return err
	}

	vm, err := conn.DomainDefineXML(xml)
	if err != nil {
		log.Warnf("Failed to create the VM: %s", err)
		return err
	}
	d.VM = &vm
	d.vmLoaded = true
	log.Debugf("Adding the file: %s", filepath.Join(d.ResolveStorePath("."), fmt.Sprintf(".%s-exist", d.MachineName)))
	_, _ = os.OpenFile(filepath.Join(d.ResolveStorePath("."), fmt.Sprintf(".%s-exist", d.MachineName)), os.O_RDONLY|os.O_CREATE, 0666)

	return d.Start()
}

func domainXML(d *Driver) (string, error) {
	tmpl, err := template.New("domain").Parse(DomainTemplate)
	if err != nil {
		return "", err
	}

	config := DomainConfig{
		DomainName: d.MachineName,
		Memory:     d.Memory,
		CPU:        d.CPU,
		CacheMode:  d.CacheMode,
		IOMode:     d.IOMode,
		DiskPath:   d.DiskPath,
	}
	if d.Network != "" {
		config.ExtraDevices = append(config.ExtraDevices, NetworkDevice(d.Network))
	}
	if d.VSock {
		config.ExtraDevices = append(config.ExtraDevices, VSockDevice)
	}

	var xml bytes.Buffer
	err = tmpl.Execute(&xml, config)
	if err != nil {
		return "", err
	}
	return xml.String(), nil
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
	if err := d.validateVMRef(); err != nil {
		return err
	}
	conn, err := d.getConn()
	if err != nil {
		return err
	}
	if err := conn.DomainCreate(*d.VM); err != nil {
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
	if err := d.validateVMRef(); err != nil {
		return err
	}
	s, err := d.GetState()
	if err != nil {
		return err
	}

	if s != state.Stopped {
		conn, err := d.getConn()
		if err != nil {
			return err
		}
		err = conn.DomainShutdown(*d.VM)
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
	if err := d.validateVMRef(); err != nil {
		return err
	}
	conn, err := d.getConn()
	if err != nil {
		return err
	}
	// Note: If we switch to qcow disks instead of raw the user
	//       could take a snapshot.  If you do, then Undefine
	//       will fail unless we nuke the snapshots first
	_ = conn.DomainDestroy(*d.VM) // Ignore errors
	return conn.DomainUndefine(*d.VM)
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
	if err := d.validateVMRef(); err != nil {
		return err
	}
	conn, err := d.getConn()
	if err != nil {
		return err
	}
	return conn.DomainDestroy(*d.VM)
}

func (d *Driver) GetState() (state.State, error) {
	log.Debugf("Getting current state...")
	if err := d.validateVMRef(); err != nil {
		return state.None, err
	}
	conn, err := d.getConn()
	if err != nil {
		return state.Error, err
	}
	virState, _, err := conn.DomainGetState(*d.VM, 0)
	if err != nil {
		return state.None, err
	}
	switch virState {
	case 0:
		return state.None, nil
	case 1:
		return state.Running, nil
	case 2:
		// TODO - Not really correct, but does it matter?
		return state.Error, nil
	case 3:
		return state.Paused, nil
	case 4:
		return state.Stopped, nil
	case 5:
		return state.Stopped, nil
	case 6:
		return state.Error, nil
	case 7:
		return state.Saved, nil
	}
	return state.None, nil
}

func (d *Driver) validateVMRef() error {
	if !d.vmLoaded {
		log.Debugf("Fetching VM...")
		conn, err := d.getConn()
		if err != nil {
			return err
		}
		vm, err := conn.DomainLookupByName(d.MachineName)
		if err != nil {
			log.Warnf("Failed to fetch machine")
			return fmt.Errorf("Failed to fetch machine '%s'", d.MachineName)
		}
		d.VM = &vm
		d.vmLoaded = true
	}
	return nil
}

// This implementation is specific to default networking in libvirt
// with dnsmasq
func (d *Driver) getMAC() (string, error) {
	if err := d.validateVMRef(); err != nil {
		return "", err
	}
	conn, err := d.getConn()
	if err != nil {
		return "", err
	}
	xmldoc, err := conn.DomainGetXMLDesc(*d.VM, 0)
	if err != nil {
		return "", err
	}
	/* XML structure:
	<domain>
	    ...
	    <devices>
	        ...
	        <interface type='network'>
	            ...
	            <mac address='52:54:00:d2:3f:ba'/>
	            ...
	        </interface>
	        ...
	*/
	type Mac struct {
		Address string `xml:"address,attr"`
	}
	type Source struct {
		Network string `xml:"network,attr"`
	}
	type Interface struct {
		Type   string `xml:"type,attr"`
		Mac    Mac    `xml:"mac"`
		Source Source `xml:"source"`
	}
	type Devices struct {
		Interfaces []Interface `xml:"interface"`
	}
	type Domain struct {
		Devices Devices `xml:"devices"`
	}

	var dom Domain
	err = xml.Unmarshal([]byte(xmldoc), &dom)
	if err != nil {
		return "", err
	}

	return dom.Devices.Interfaces[0].Mac.Address, nil
}

func (d *Driver) getIPByMacFromSettings(mac string) (string, error) {
	conn, err := d.getConn()
	if err != nil {
		return "", err
	}
	network, err := conn.NetworkLookupByName(d.Network)
	if err != nil {
		log.Warnf("Failed to find network: %s", err)
		return "", err
	}
	bridgeName, err := conn.NetworkGetBridgeName(network)
	if err != nil {
		log.Warnf("Failed to get network bridge: %s", err)
		return "", err
	}
	statusFile := fmt.Sprintf(dnsmasqStatus, bridgeName)
	data, err := ioutil.ReadFile(statusFile)
	if err != nil {
		log.Warnf("Failed to read status file: %s", err)
		return "", err
	}
	type Lease struct {
		IPAddress  string `json:"ip-address"`
		MacAddress string `json:"mac-address"`
		// Other unused fields omitted
	}
	var s []Lease

	// In case of status file is empty then don't try to unmarshal data
	if len(data) == 0 {
		return "", nil
	}

	err = json.Unmarshal(data, &s)
	if err != nil {
		log.Warnf("Failed to decode dnsmasq lease status: %s", err)
		return "", err
	}
	ipAddr := ""
	for _, value := range s {
		if strings.EqualFold(value.MacAddress, mac) {
			// If there are multiple entries,
			// the last one is the most current
			ipAddr = value.IPAddress
		}
	}
	if ipAddr != "" {
		log.Debugf("IP address: %s", ipAddr)
	}
	return ipAddr, nil
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
