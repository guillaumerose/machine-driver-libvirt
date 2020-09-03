package libvirt

import (
	"bytes"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
)

func TestTemplating(t *testing.T) {
	tmpl, err := template.New("domain").Parse(DomainTemplate)
	assert.NoError(t, err)

	var xml bytes.Buffer
	assert.NoError(t, tmpl.Execute(&xml, DomainConfig{
		DomainName: "domain",
		Memory:     4096,
		CPU:        4,
		CacheMode:  "default",
		IOMode:     "threads",
		DiskPath:   "disk_path",
		Network:    "network",
	}))

	assert.Equal(t, `<domain type='kvm'>
  <name>domain</name>
  <memory unit='MB'>4096</memory>
  <vcpu placement='static'>4</vcpu>
  <features><acpi/><apic/><pae/></features>
  <cpu mode='host-passthrough'>
    <feature policy="disable" name="rdrand"/>
  </cpu>
  <os>
    <type arch='x86_64'>hvm</type>
    <boot dev='hd'/>
    <bootmenu enable='no'/>
  </os>
  <features>
    <acpi/>
    <apic/>
    <pae/>
  </features>
  <clock offset='utc'/>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='default' io='threads' />
      <source file='disk_path'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <graphics type='vnc' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>
    <interface type='network'>
      <mac address='52:fd:fc:07:21:82'/>
      <source network='network'/>
      <model type='virtio'/>
    </interface>
    <console type='pty'></console>
    <channel type='pty'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
    <rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
  </devices>
</domain>`, xml.String())
}

func TestVSockTemplating(t *testing.T) {
	tmpl, err := template.New("domain").Parse(DomainTemplate)
	assert.NoError(t, err)

	var xml bytes.Buffer
	config := DomainConfig{
		DomainName:   "domain",
		Memory:       4096,
		CPU:          4,
		CacheMode:    "default",
		IOMode:       "threads",
		DiskPath:     "disk_path",
		Network:      "network",
		ExtraDevices: []string{VSockDevice},
	}
	assert.NoError(t, tmpl.Execute(&xml, config))
	assert.Regexp(t, `(?s)<devices>(.*?)<vsock model='virtio'><cid auto='yes'/></vsock>(.*?)</devices>`, xml.String())
}
