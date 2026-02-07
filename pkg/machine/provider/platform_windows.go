package provider

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/containers/libhvee/pkg/hypervctl"
	"github.com/containers/podman/v5/pkg/machine/vmconfigs"
	"github.com/containers/podman/v5/pkg/machine/vbox"
	"github.com/containers/podman/v5/pkg/machine/wsl"
	"github.com/containers/podman/v5/pkg/machine/wsl/wutil"

	"github.com/containers/podman/v5/pkg/machine/define"
	"github.com/containers/podman/v5/pkg/machine/hyperv"
	"github.com/sirupsen/logrus"
	"go.podman.io/common/pkg/config"
)

func Get() (vmconfigs.VMProvider, error) {
	cfg, err := config.Default()
	if err != nil {
		return nil, err
	}
	provider := cfg.Machine.Provider
	if providerOverride, found := os.LookupEnv("CONTAINERS_MACHINE_PROVIDER"); found {
		provider = providerOverride
	}
	resolvedVMType, err := define.ParseVMType(provider, define.WSLVirt)
	if err != nil {
		return nil, err
	}

	logrus.Debugf("Using Podman machine with `%s` virtualization provider", resolvedVMType.String())
	switch resolvedVMType {
	case define.WSLVirt:
		return new(wsl.WSLStubber), nil
	case define.HyperVVirt:
		if !wsl.HasAdminRights() {
			return nil, fmt.Errorf("hyperv machines require admin authority")
		}
		return new(hyperv.HyperVStubber), nil
	case define.VBoxVirt:
		return new(vbox.VBoxStubber), nil
	default:
		return nil, fmt.Errorf("unsupported virtualization provider: `%s`", resolvedVMType.String())
	}
}

func GetAll() []vmconfigs.VMProvider {
	return []vmconfigs.VMProvider{
		new(wsl.WSLStubber),
		new(hyperv.HyperVStubber),
		new(vbox.VBoxStubber),
	}
}

// SupportedProviders returns the providers that are supported on the host operating system
func SupportedProviders() []define.VMType {
	return []define.VMType{define.HyperVVirt, define.WSLVirt, define.VBoxVirt}
}

func IsInstalled(provider define.VMType) (bool, error) {
	switch provider {
	case define.WSLVirt:
		return wutil.IsWSLInstalled(), nil
	case define.HyperVVirt:
		service, err := hypervctl.NewLocalHyperVService()
		if err == nil {
			return true, nil
		}
		if service != nil {
			defer service.Close()
		}
		return false, nil
	case define.VBoxVirt:
		_, err := exec.LookPath("VBoxManage")
		if err == nil {
			return true, nil
		}
		_, err = exec.LookPath("VBoxManage.exe")
		return err == nil, nil
	default:
		return false, nil
	}
}

// HasPermsForProvider returns whether the host operating system has the proper permissions to use the given provider
func HasPermsForProvider(provider define.VMType) bool {
	switch provider {
	case define.QemuVirt:
		fallthrough
	case define.AppleHvVirt:
		return false
	case define.HyperVVirt:
		return wsl.HasAdminRights()
	case define.VBoxVirt:
		return true
	}

	return true
}
