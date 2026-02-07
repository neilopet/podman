//go:build windows

package vbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	gvproxy "github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/podman/v5/pkg/machine"
	"github.com/containers/podman/v5/pkg/machine/define"
	"github.com/containers/podman/v5/pkg/machine/ignition"
	"github.com/containers/podman/v5/pkg/machine/vmconfigs"
	"github.com/sirupsen/logrus"
)

type VBoxStubber struct {
	vmconfigs.VirtualBoxConfig
}

func (v VBoxStubber) VMType() define.VMType {
	return define.VBoxVirt
}

func (v VBoxStubber) UserModeNetworkEnabled(_ *vmconfigs.MachineConfig) bool {
	return false
}

func (v VBoxStubber) UseProviderNetworkSetup() bool {
	return false
}

func (v VBoxStubber) RequireExclusiveActive() bool {
	return false
}

func (v VBoxStubber) CreateVM(_ define.CreateVMOpts, mc *vmconfigs.MachineConfig, _ *ignition.IgnitionBuilder) error {
	var err error
	callbackFuncs := machine.CleanUp()
	defer callbackFuncs.CleanIfErr(&err)
	go callbackFuncs.CleanOnSignal()

	if mc.VBoxHypervisor == nil {
		return errors.New("VBoxHypervisor config is nil")
	}

	vboxManagePath := mc.VBoxHypervisor.VBoxManagePath
	vmName := mc.VBoxHypervisor.VMName
	if vmName == "" {
		vmName = mc.Name
	}

	// Check if a VM with this name already exists in VirtualBox — if so, adopt it
	exists, err := vmExists(vboxManagePath, vmName)
	if err != nil {
		return fmt.Errorf("checking if VM exists: %w", err)
	}

	if exists {
		logrus.Infof("VirtualBox VM %q already exists, adopting it", vmName)
		mc.VBoxHypervisor.VMName = vmName

		// Configure shared folders (VM must be stopped for sharedfolder add)
		if len(mc.Mounts) > 0 {
			// Remove any existing shared folders to avoid conflicts
			if err := removeAllSharedFolders(vboxManagePath, vmName); err != nil {
				logrus.Warnf("Failed to clean existing shared folders: %v", err)
			}

			for _, mnt := range mc.Mounts {
				name := mnt.Tag
				if name == "" {
					name = mnt.Target
				}
				logrus.Infof("Adding shared folder: %s -> %s (host: %s)", name, mnt.Target, mnt.Source)
				if err := addSharedFolder(vboxManagePath, vmName, name, mnt.Source, mnt.Target, mnt.ReadOnly); err != nil {
					return fmt.Errorf("adding shared folder %s: %w", name, err)
				}
			}
		}

		return nil
	}

	return fmt.Errorf("VirtualBox VM %q does not exist. The VBox provider adopts existing VMs — please create the VM in VirtualBox first", vmName)
}

func (v VBoxStubber) PrepareIgnition(mc *vmconfigs.MachineConfig, _ *ignition.IgnitionBuilder) (*ignition.ReadyUnitOpts, error) {
	// VBox VMs are pre-installed, no ignition needed
	// Preserve VMName if it was already set (e.g., from --image flag)
	existingVMName := ""
	existingHostOnlyIP := ""
	if mc.VBoxHypervisor != nil {
		existingVMName = mc.VBoxHypervisor.VMName
		existingHostOnlyIP = mc.VBoxHypervisor.HostOnlyIP
	}

	mc.VBoxHypervisor = new(vmconfigs.VirtualBoxConfig)
	mc.VBoxHypervisor.HeadlessMode = true

	// Find VBoxManage
	vboxManagePath, err := findVBoxManage()
	if err != nil {
		return nil, err
	}
	mc.VBoxHypervisor.VBoxManagePath = vboxManagePath

	// Restore preserved VM name or default to podman machine name
	if existingVMName != "" {
		mc.VBoxHypervisor.VMName = existingVMName
	} else {
		mc.VBoxHypervisor.VMName = mc.Name
	}
	if existingHostOnlyIP != "" {
		mc.VBoxHypervisor.HostOnlyIP = existingHostOnlyIP
	}

	return nil, nil
}

func (v VBoxStubber) Exists(name string) (bool, error) {
	vboxManagePath, err := findVBoxManage()
	if err != nil {
		return false, err
	}
	return vmExists(vboxManagePath, name)
}

func (v VBoxStubber) MountType() vmconfigs.VolumeMountType {
	return vmconfigs.Unknown
}

func (v VBoxStubber) MountVolumesToVM(_ *vmconfigs.MachineConfig, _ bool) error {
	// VBox VMs are pre-existing with their own filesystems.
	// Shared folder management should be done directly in VirtualBox.
	return nil
}

func (v VBoxStubber) Remove(mc *vmconfigs.MachineConfig) ([]string, func() error, error) {
	if mc.VBoxHypervisor == nil {
		return nil, nil, errors.New("VBoxHypervisor config is nil")
	}

	rmFunc := func() error {
		// VBox VMs are pre-existing — do not unregister from VirtualBox.
		// Podman only removes its own config files.
		return nil
	}
	return []string{}, rmFunc, nil
}

func (v VBoxStubber) RemoveAndCleanMachines(_ *define.MachineDirs) error {
	return nil
}

func (v VBoxStubber) SetProviderAttrs(mc *vmconfigs.MachineConfig, opts define.SetOptions) error {
	if mc.VBoxHypervisor == nil {
		return errors.New("VBoxHypervisor config is nil")
	}

	vmName := mc.VBoxHypervisor.VMName
	if vmName == "" {
		vmName = mc.Name
	}
	vboxManagePath := mc.VBoxHypervisor.VBoxManagePath

	// VM must be stopped to modify settings
	state, err := getVMState(vboxManagePath, vmName)
	if err != nil {
		return err
	}
	if state != define.Stopped {
		return errors.New("unable to change settings unless VM is stopped")
	}

	if opts.Rootful != nil && mc.HostUser.Rootful != *opts.Rootful {
		if err := mc.SetRootful(*opts.Rootful); err != nil {
			return err
		}
	}

	var modifyArgs []string
	if opts.CPUs != nil {
		modifyArgs = append(modifyArgs, "--cpus", fmt.Sprintf("%d", *opts.CPUs))
	}
	if opts.Memory != nil {
		modifyArgs = append(modifyArgs, "--memory", fmt.Sprintf("%d", uint64(*opts.Memory)))
	}
	if len(modifyArgs) > 0 {
		if err := modifyVM(vboxManagePath, vmName, modifyArgs...); err != nil {
			return fmt.Errorf("modifying VM settings: %w", err)
		}
	}

	if opts.DiskSize != nil {
		logrus.Warnf("Disk resize is not supported for VirtualBox VMs via podman machine set")
	}
	if opts.USBs != nil {
		return fmt.Errorf("changing USBs not yet supported for VirtualBox machines")
	}
	return nil
}

func (v VBoxStubber) StartNetworking(_ *vmconfigs.MachineConfig, _ *gvproxy.GvproxyCommand) error {
	// VBox handles its own networking (bridged + host-only); no gvproxy endpoint needed
	return nil
}

func (v VBoxStubber) PostStartNetworking(mc *vmconfigs.MachineConfig, _ bool) error {
	if mc.VBoxHypervisor == nil {
		return errors.New("VBoxHypervisor config is nil")
	}

	// Wait for SSH to become available
	host := mc.VBoxHypervisor.HostOnlyIP
	if host == "" {
		host = "127.0.0.1"
	}
	port := mc.SSH.Port
	if port == 0 {
		port = 22
	}

	logrus.Infof("Waiting for SSH to be available at %s:%d", host, port)
	if err := waitForSSH(host, port, 120*time.Second); err != nil {
		return fmt.Errorf("VM started but SSH not available: %w", err)
	}

	// Fix mount point parent directory permissions.
	// VBox automounter creates parent dirs as root:root 0750, which prevents
	// non-root users from traversing to the mount point. Make them world-readable.
	if len(mc.Mounts) > 0 {
		username := mc.SSH.RemoteUsername
		identity := mc.SSH.IdentityPath
		for _, mnt := range mc.Mounts {
			parent := filepath.ToSlash(filepath.Dir(filepath.Clean(mnt.Target)))
			if parent == "/" || parent == "." || parent == "/mnt" {
				continue
			}
			// Use sudo with stdin password. The SSH user's password typically matches
			// the username on default Kali installs (kali/kali).
			cmd := fmt.Sprintf("echo %s | sudo -S chmod 755 %s 2>/dev/null", username, parent)
			if err := machine.CommonSSHSilent(username, identity, mc.Name, host, port, []string{cmd}); err != nil {
				logrus.Warnf("Failed to fix permissions on %s: %v (you may need to manually run: sudo chmod 755 %s)", parent, err, parent)
			}
		}
	}

	return nil
}

func (v VBoxStubber) StartVM(mc *vmconfigs.MachineConfig) (func() error, func() error, error) {
	var err error

	if mc.VBoxHypervisor == nil {
		return nil, nil, errors.New("VBoxHypervisor config is nil")
	}

	callbackFuncs := machine.CleanUp()
	defer callbackFuncs.CleanIfErr(&err)
	go callbackFuncs.CleanOnSignal()

	vmName := mc.VBoxHypervisor.VMName
	if vmName == "" {
		vmName = mc.Name
	}
	vboxManagePath := mc.VBoxHypervisor.VBoxManagePath

	err = startVM(vboxManagePath, vmName, mc.VBoxHypervisor.HeadlessMode)
	if err != nil {
		return nil, nil, err
	}

	stopCallback := func() error {
		return stopVM(vboxManagePath, vmName, true)
	}
	callbackFuncs.Add(stopCallback)

	// Return a waitReady function that waits for SSH
	waitReady := func() error {
		host := mc.VBoxHypervisor.HostOnlyIP
		if host == "" {
			host = "127.0.0.1"
		}
		port := mc.SSH.Port
		if port == 0 {
			port = 22
		}
		return waitForSSH(host, port, 120*time.Second)
	}

	return nil, waitReady, nil
}

func (v VBoxStubber) State(mc *vmconfigs.MachineConfig, _ bool) (define.Status, error) {
	if mc.VBoxHypervisor == nil {
		return define.Unknown, errors.New("VBoxHypervisor config is nil")
	}

	vmName := mc.VBoxHypervisor.VMName
	if vmName == "" {
		vmName = mc.Name
	}
	return getVMState(mc.VBoxHypervisor.VBoxManagePath, vmName)
}

func (v VBoxStubber) StopVM(mc *vmconfigs.MachineConfig, hardStop bool) error {
	if mc.VBoxHypervisor == nil {
		return errors.New("VBoxHypervisor config is nil")
	}

	vmName := mc.VBoxHypervisor.VMName
	if vmName == "" {
		vmName = mc.Name
	}
	vboxManagePath := mc.VBoxHypervisor.VBoxManagePath

	state, err := getVMState(vboxManagePath, vmName)
	if err != nil {
		return err
	}
	if state == define.Stopped {
		return nil
	}
	if state != define.Running {
		return fmt.Errorf("VM is in unexpected state: %s", state)
	}

	return stopVM(vboxManagePath, vmName, hardStop)
}

func (v VBoxStubber) StopHostNetworking(mc *vmconfigs.MachineConfig, vmType define.VMType) error {
	err := machine.StopWinProxy(mc.Name, vmType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not stop API forwarding service (win-sshproxy.exe): %s\n", err.Error())
	}
	return nil
}

func (v VBoxStubber) UpdateSSHPort(mc *vmconfigs.MachineConfig, port int) error {
	if mc.VBoxHypervisor == nil {
		return errors.New("VBoxHypervisor config is nil")
	}
	mc.SSH.Port = port
	return nil
}

func (v VBoxStubber) GetRosetta(_ *vmconfigs.MachineConfig) (bool, error) {
	return false, nil
}
