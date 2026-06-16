//go:build integration

// Integration tests exercise the service layer against a LIVE local Incus daemon
// that can boot VMs. They are slow and environment-specific, so they are gated
// behind the "integration" build tag:
//
//	go test -tags integration ./internal/incus/...
//
// They SKIP (rather than fail) when there is no reachable daemon or no /dev/kvm, so
// CI on a runner without nested virtualization goes neutral instead of red.
package incus

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

const testImageAlias = "ubuntu/24.04/cloud"

func requireLiveVMHost(t *testing.T) *Client {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm: skipping VM integration test")
	}
	c, err := Connect()
	if err != nil {
		t.Skipf("no reachable Incus daemon: %v", err)
	}
	return c
}

func TestLiveLifecycle(t *testing.T) {
	c := requireLiveVMHost(t)
	defer c.Disconnect()
	ctx := context.Background()

	// Images: the browse path must return VM-capable images for this host's arch.
	imgs, err := c.ListVMImages()
	if err != nil {
		t.Fatalf("ListVMImages: %v", err)
	}
	if len(imgs) == 0 {
		t.Fatal("ListVMImages returned no VM images")
	}
	for _, im := range imgs[:min(3, len(imgs))] {
		t.Logf("image: %-28s cloud=%v %s", im.Alias, im.Cloud, im.Fingerprint[:12])
	}

	name := fmt.Sprintf("itest-%d", time.Now().UnixNano()%100000)
	t.Cleanup(func() { _ = c.Delete(context.Background(), name) }) // force cleanup even on failure

	// Create with cloud-init + limits + a custom disk size (exercises pool resolution).
	spec := CreateSpec{
		Name:          name,
		ImageAlias:    testImageAlias,
		CPU:           "1",
		Memory:        "512MiB",
		DiskSize:      "10GiB",
		CloudInitUser: "#cloud-config\nruncmd:\n  - touch /run/itest-ok\n",
	}
	if err := c.CreateVM(ctx, spec); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	vm, err := c.GetVM(name)
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if !vm.Running() {
		t.Fatalf("VM %q status=%q, want Running", name, vm.Status)
	}
	if vm.CPULimit != "1" || vm.MemLimit != "512MiB" {
		t.Errorf("limits = cpu:%q mem:%q, want 1/512MiB", vm.CPULimit, vm.MemLimit)
	}

	// Snapshot + verify it appears.
	if err := c.Snapshot(ctx, name, "snap0"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	vm, _ = c.GetVM(name)
	if len(vm.Snapshots) != 1 || vm.Snapshots[0] != "snap0" {
		t.Fatalf("snapshots = %v, want [snap0]", vm.Snapshots)
	}

	if err := c.RestoreSnapshot(ctx, name, "snap0"); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	// Edit limits on the existing (running) VM.
	if err := c.SetLimits(ctx, name, "2", ""); err != nil {
		t.Fatalf("SetLimits: %v", err)
	}
	vm, _ = c.GetVM(name)
	if vm.CPULimit != "2" {
		t.Errorf("after SetLimits cpu = %q, want 2", vm.CPULimit)
	}

	if err := c.DeleteSnapshot(ctx, name, "snap0"); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}

	// Console log should be readable for a booted VM.
	if _, err := c.ConsoleLog(name); err != nil {
		t.Errorf("ConsoleLog: %v", err)
	}

	// Delete (stops first since it is running).
	if err := c.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
