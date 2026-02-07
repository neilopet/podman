package vmconfigs

import (
	"github.com/containers/podman/v5/pkg/machine/define"
	"github.com/containers/podman/v5/pkg/machine/hyperv/vsock"
	"github.com/containers/podman/v5/pkg/machine/qemu/command"
)

type HyperVConfig struct {
	// ReadyVSock is the pipeline for the guest to alert the host
	// it is running
	ReadyVsock vsock.HVSockRegistryEntry
	// NetworkVSock is for the user networking
	NetworkVSock vsock.HVSockRegistryEntry
}

type WSLConfig struct {
	// Uses usermode networking
	UserModeNetworking bool
}

type QEMUConfig struct {
	// QMPMonitor is the qemu monitor object for sending commands
	QMPMonitor command.Monitor
	// QEMUPidPath is where to write the PID for QEMU when running
	QEMUPidPath *define.VMFile
}

type VirtualBoxConfig struct {
	// VMName is the VirtualBox VM name (may differ from podman machine name)
	VMName string
	// VBoxManagePath is the path to VBoxManage.exe
	VBoxManagePath string
	// HeadlessMode controls whether to start with --type headless vs gui
	HeadlessMode bool
	// HostOnlyAdapter is the name of the host-only adapter for stable IP
	HostOnlyAdapter string
	// HostOnlyIP is the static IP on the host-only network
	HostOnlyIP string
	// BridgedAdapter is the name of the bridged adapter for internet
	BridgedAdapter string
}

// Stubs
type AppleHVConfig struct{}
type LibKrunConfig struct{}

func getHostUID() int {
	return 1000
}
