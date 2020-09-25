module github.com/code-ready/machine-driver-libvirt

go 1.14

require (
	github.com/code-ready/machine v0.0.0-20200903081832-ffa0b31fbdb3
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/digitalocean/go-libvirt v0.0.0-20200810224808-b9c702499bf7
	github.com/stretchr/testify v1.6.1
	gopkg.in/yaml.v3 v3.0.0-20200615113413-eeeca48fe776 // indirect
)

replace github.com/digitalocean/go-libvirt => github.com/guillaumerose/go-libvirt v0.0.0-20200925082511-654bed68cfba
