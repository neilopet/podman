//go:build windows

package vbox

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/containers/podman/v5/pkg/machine/define"
	"github.com/sirupsen/logrus"
)

const defaultVBoxManagePath = "C:\\Program Files\\Oracle\\VirtualBox\\VBoxManage.exe"

// findVBoxManage locates VBoxManage.exe, checking PATH first then the default install location
func findVBoxManage() (string, error) {
	path, err := exec.LookPath("VBoxManage.exe")
	if err == nil {
		return path, nil
	}
	path, err = exec.LookPath("VBoxManage")
	if err == nil {
		return path, nil
	}
	// Check default installation path
	cmd := exec.Command(defaultVBoxManagePath, "--version")
	if err := cmd.Run(); err == nil {
		return defaultVBoxManagePath, nil
	}
	return "", fmt.Errorf("VBoxManage not found in PATH or at %s", defaultVBoxManagePath)
}

// runVBoxManage executes a VBoxManage command and returns stdout
func runVBoxManage(vboxManagePath string, args ...string) (string, error) {
	if vboxManagePath == "" {
		var err error
		vboxManagePath, err = findVBoxManage()
		if err != nil {
			return "", err
		}
	}
	logrus.Debugf("Running VBoxManage: %s %v", vboxManagePath, args)
	cmd := exec.Command(vboxManagePath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("VBoxManage %s failed: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// getVMState queries VBoxManage for the VM state and returns a define.Status
func getVMState(vboxManagePath, vmName string) (define.Status, error) {
	out, err := runVBoxManage(vboxManagePath, "showvminfo", vmName, "--machinereadable")
	if err != nil {
		return define.Unknown, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "VMState=") {
			state := strings.Trim(strings.TrimPrefix(line, "VMState="), "\"")
			switch state {
			case "running":
				return define.Running, nil
			case "poweroff", "aborted", "saved":
				return define.Stopped, nil
			case "starting", "restoring":
				return define.Starting, nil
			default:
				return define.Unknown, nil
			}
		}
	}
	return define.Unknown, fmt.Errorf("could not determine VM state for %s", vmName)
}

// vmExists checks if a VM with the given name is registered in VirtualBox
func vmExists(vboxManagePath, vmName string) (bool, error) {
	out, err := runVBoxManage(vboxManagePath, "list", "vms")
	if err != nil {
		return false, err
	}
	// VBoxManage list vms outputs lines like: "vmname" {uuid}
	search := fmt.Sprintf("\"%s\"", vmName)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, search) {
			return true, nil
		}
	}
	return false, nil
}

// waitForSSH waits until an SSH connection can be established to the given address
func waitForSSH(host string, port int, timeout time.Duration) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	deadline := time.Now().Add(timeout)
	logrus.Debugf("Waiting for SSH at %s (timeout %s)", addr, timeout)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			logrus.Debugf("SSH is available at %s", addr)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for SSH at %s after %s", addr, timeout)
}

// startVM starts a VirtualBox VM in the specified mode (headless or gui)
func startVM(vboxManagePath, vmName string, headless bool) error {
	vmType := "gui"
	if headless {
		vmType = "headless"
	}
	_, err := runVBoxManage(vboxManagePath, "startvm", vmName, "--type", vmType)
	return err
}

// stopVM stops a VirtualBox VM, using ACPI powerbutton for soft shutdown or poweroff for hard stop
func stopVM(vboxManagePath, vmName string, hardStop bool) error {
	if hardStop {
		_, err := runVBoxManage(vboxManagePath, "controlvm", vmName, "poweroff")
		return err
	}
	_, err := runVBoxManage(vboxManagePath, "controlvm", vmName, "acpipowerbutton")
	return err
}

// modifyVM modifies VM settings (must be called when VM is stopped)
func modifyVM(vboxManagePath, vmName string, args ...string) error {
	fullArgs := append([]string{"modifyvm", vmName}, args...)
	_, err := runVBoxManage(vboxManagePath, fullArgs...)
	return err
}

// addSharedFolder adds a VirtualBox shared folder with automount to a stopped VM.
// The folder is mounted at mountPoint inside the guest via Guest Additions.
func addSharedFolder(vboxManagePath, vmName, name, hostPath, mountPoint string, readOnly bool) error {
	args := []string{"sharedfolder", "add", vmName,
		"--name", name,
		"--hostpath", hostPath,
		"--automount",
		"--auto-mount-point", mountPoint,
	}
	if readOnly {
		args = append(args, "--readonly")
	}
	_, err := runVBoxManage(vboxManagePath, args...)
	return err
}

// removeSharedFolder removes a shared folder from a VM
func removeSharedFolder(vboxManagePath, vmName, name string) error {
	_, err := runVBoxManage(vboxManagePath, "sharedfolder", "remove", vmName, "--name", name)
	return err
}

// listSharedFolders returns the names of shared folders configured on a VM
func listSharedFolders(vboxManagePath, vmName string) ([]string, error) {
	out, err := runVBoxManage(vboxManagePath, "showvminfo", vmName, "--machinereadable")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// Lines like: SharedFolderNameMachineMapping1="sharename"
		if strings.HasPrefix(line, "SharedFolderNameMachineMapping") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				name := strings.Trim(parts[1], "\"")
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// removeAllSharedFolders removes all shared folders from a VM
func removeAllSharedFolders(vboxManagePath, vmName string) error {
	names, err := listSharedFolders(vboxManagePath, vmName)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := removeSharedFolder(vboxManagePath, vmName, name); err != nil {
			logrus.Warnf("Failed to remove shared folder %q: %v", name, err)
		}
	}
	return nil
}

