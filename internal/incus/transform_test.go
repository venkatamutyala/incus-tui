package incus

import (
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

func TestToVM(t *testing.T) {
	f := &api.InstanceFull{}
	f.Name = "vm1"
	f.Status = "Running"
	f.StatusCode = api.Running
	f.Type = "virtual-machine"
	f.CreatedAt = time.Now().Add(-time.Hour)
	f.Config = map[string]string{"limits.cpu": "2", "limits.memory": "2GiB", "image.description": "Ubuntu"}
	f.State = &api.InstanceState{
		Processes: 5,
		Network: map[string]api.InstanceStateNetwork{
			"lo":   {Addresses: []api.InstanceStateNetworkAddress{{Family: "inet", Address: "127.0.0.1", Scope: "local"}}},
			"eth0": {Addresses: []api.InstanceStateNetworkAddress{{Family: "inet", Address: "10.0.0.5", Scope: "global"}}},
		},
	}
	f.Snapshots = []api.InstanceSnapshot{{Name: "s1"}}

	vm := toVM(f)
	if vm.IPv4 != "10.0.0.5" {
		t.Errorf("IPv4 = %q, want 10.0.0.5", vm.IPv4)
	}
	if !vm.AgentReady {
		t.Error("AgentReady should be true when Processes > 0")
	}
	if vm.CPULimit != "2" || vm.MemLimit != "2GiB" {
		t.Errorf("limits = %q/%q, want 2/2GiB", vm.CPULimit, vm.MemLimit)
	}
	if vm.Image != "Ubuntu" {
		t.Errorf("Image = %q, want Ubuntu", vm.Image)
	}
	if len(vm.Snapshots) != 1 || vm.Snapshots[0] != "s1" {
		t.Errorf("Snapshots = %v, want [s1]", vm.Snapshots)
	}
}

func TestToVMAgentNotReady(t *testing.T) {
	f := &api.InstanceFull{}
	f.StatusCode = api.Running
	f.State = &api.InstanceState{Processes: -1} // agent hasn't connected yet
	if toVM(f).AgentReady {
		t.Error("AgentReady should be false when Processes == -1")
	}

	g := &api.InstanceFull{}
	g.StatusCode = api.Running // State nil
	if toVM(g).AgentReady {
		t.Error("AgentReady should be false when State is nil")
	}
}

func TestParseLifecycle(t *testing.T) {
	mk := func(meta string) api.Event { return api.Event{Type: "lifecycle", Metadata: []byte(meta)} }

	ev := parseLifecycle(mk(`{"action":"instance-started","name":"web"}`))
	if ev.Instance != "web" || ev.Action != "instance-started" {
		t.Errorf("got %+v, want web/instance-started", ev)
	}
	// Name empty → fall back to parsing the Source path.
	ev = parseLifecycle(mk(`{"action":"instance-deleted","source":"/1.0/instances/db"}`))
	if ev.Instance != "db" {
		t.Errorf("source fallback Instance = %q, want db", ev.Instance)
	}
}

func TestImageLabel(t *testing.T) {
	withAlias := &api.Image{Aliases: []api.ImageAlias{{Name: "ubuntu/24.04/cloud"}}}
	if got := imageLabel(withAlias); got != "ubuntu/24.04/cloud" {
		t.Errorf("alias label = %q", got)
	}
	noAlias := &api.Image{Fingerprint: "abcdef1234567890"}
	noAlias.Properties = map[string]string{"os": "ubuntu", "release": "noble", "variant": "cloud"}
	if got := imageLabel(noAlias); got != "ubuntu/noble/cloud" {
		t.Errorf("properties label = %q, want ubuntu/noble/cloud", got)
	}
}

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{"amd64": "x86_64", "x86_64": "x86_64", "arm64": "aarch64", "aarch64": "aarch64", "riscv64": "riscv64"}
	for in, want := range cases {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}
