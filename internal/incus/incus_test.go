package incus

import (
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

func TestValidateCloudInit(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty is ok", "", false},
		{"whitespace is ok", "   \n  ", false},
		{"valid cloud-config", "#cloud-config\npackages:\n  - htop\n", false},
		{"missing header", "packages:\n  - htop\n", true},
		{"malformed yaml", "#cloud-config\npackages: [unterminated\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCloudInit(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateCloudInit(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}
}

// Every baked-in starter must be valid cloud-init or the wizard would seed a template
// that fails ValidateCloudInit at launch.
func TestStarterTemplatesAreValid(t *testing.T) {
	for name, body := range starterTemplates {
		if err := ValidateCloudInit(body); err != nil {
			t.Errorf("starter %s is not valid cloud-init: %v", name, err)
		}
	}
	if _, ok := starterTemplates["glueops-work-cde.yaml"]; !ok {
		t.Error("glueops-work-cde.yaml starter is missing")
	}
}

func TestTemplateName(t *testing.T) {
	// "# name:" override wins over the filename base, and may contain spaces and "/".
	if got := templateName("glueops-work-cde", starterTemplates["glueops-work-cde.yaml"]); got != "GluOps / Work CDE" {
		t.Errorf("name = %q, want %q", got, "GluOps / Work CDE")
	}
	// No override → fall back to the filename base.
	if got := templateName("minimal", starterTemplates["minimal.yaml"]); got != "minimal" {
		t.Errorf("fallback name = %q, want minimal", got)
	}
	// A "# name:" that appears only in the YAML body (not the header) is ignored.
	body := "#cloud-config\nruncmd:\n  - echo '# name: not-a-name'\n"
	if got := templateName("base", body); got != "base" {
		t.Errorf("body-only name = %q, want base", got)
	}
}

func TestPrimaryIPv4(t *testing.T) {
	st := &api.InstanceState{
		Network: map[string]api.InstanceStateNetwork{
			"lo": {Addresses: []api.InstanceStateNetworkAddress{
				{Family: "inet", Address: "127.0.0.1", Scope: "local"},
			}},
			"enp5s0": {Addresses: []api.InstanceStateNetworkAddress{
				{Family: "inet6", Address: "fe80::1", Scope: "link"},
				{Family: "inet", Address: "10.241.140.23", Scope: "global"},
			}},
		},
	}
	if got := primaryIPv4(st); got != "10.241.140.23" {
		t.Fatalf("primaryIPv4 = %q, want 10.241.140.23", got)
	}

	empty := &api.InstanceState{Network: map[string]api.InstanceStateNetwork{}}
	if got := primaryIPv4(empty); got != "" {
		t.Fatalf("primaryIPv4(empty) = %q, want empty", got)
	}
}

func TestImageDescription(t *testing.T) {
	cases := []struct {
		cfg  map[string]string
		want string
	}{
		{map[string]string{"image.description": "Ubuntu 24.04"}, "Ubuntu 24.04"},
		{map[string]string{"image.os": "Ubuntu", "image.release": "noble"}, "Ubuntu noble"},
		{map[string]string{"image.os": "Debian"}, "Debian"},
		{map[string]string{}, ""},
	}
	for _, tc := range cases {
		if got := imageDescription(tc.cfg); got != tc.want {
			t.Errorf("imageDescription(%v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestInstanceFromSource(t *testing.T) {
	cases := map[string]string{
		"/1.0/instances/my-vm":           "my-vm",
		"/1.0/instances/my-vm/snapshots": "my-vm",
		"/1.0/images/abc":                "",
		"":                               "",
	}
	for src, want := range cases {
		if got := instanceFromSource(src); got != want {
			t.Errorf("instanceFromSource(%q) = %q, want %q", src, got, want)
		}
	}
}
