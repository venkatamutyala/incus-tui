package incus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Template is a reusable cloud-init user-data snippet stored on disk.
type Template struct {
	Name    string // base filename without extension
	Path    string
	Content string
}

// TemplatesDir returns the XDG config directory where cloud-init templates live.
func TemplatesDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "incus-tui", "templates")
}

// ensureTemplatesDir creates the templates directory and seeds any baked-in starter that
// is missing. Seeding by name (rather than only when the dir is empty) means a newly added
// baked-in template reaches existing installs too, while a starter the user has edited is
// left untouched (its file already exists).
func ensureTemplatesDir() (string, error) {
	dir := TemplatesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating templates dir: %w", err)
	}
	for name, body := range starterTemplates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			continue // already present (possibly user-edited) — never overwrite
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return dir, fmt.Errorf("seeding template %s: %w", name, err)
		}
	}
	return dir, nil
}

// ListTemplates returns the cloud-init templates available for launching VMs.
func ListTemplates() ([]Template, error) {
	dir, err := ensureTemplatesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading templates: %w", err)
	}
	var out []Template
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml")
		out = append(out, Template{
			Name:    templateName(base, string(content)),
			Path:    path,
			Content: string(content),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ValidateCloudInit checks that user-data is well-formed before a VM is created.
// Empty content is valid (no cloud-init). Non-empty content must parse as YAML and
// begin with the "#cloud-config" header cloud-init requires.
func ValidateCloudInit(content string) error {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "#cloud-config") {
		return fmt.Errorf("cloud-init user-data must start with #cloud-config")
	}
	var v any
	if err := yaml.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	return nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// templateName returns a template's display name: the value of a "# name: <label>"
// metadata comment in the header (so the label can contain spaces or "/" that a filename
// cannot), falling back to the filename base. Only the leading comment block is scanned.
func templateName(base, content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "# name:"); ok {
			if n := strings.TrimSpace(rest); n != "" {
				return n
			}
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			break // past the header comments into the YAML body
		}
	}
	return base
}

var starterTemplates = map[string]string{
	"minimal.yaml": `#cloud-config
# Minimal example: update apt and install a couple of packages.
package_update: true
packages:
  - htop
  - curl
`,
	"ssh-access.yaml": `#cloud-config
# Add an admin user with an SSH key. Replace the key with your own public key.
users:
  - name: admin
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: sudo
    shell: /bin/bash
    ssh_authorized_keys:
      - ssh-ed25519 AAAA...replace-with-your-public-key
`,
	"glueops-work-cde.yaml": `#cloud-config
# name: GluOps / Work CDE
# Install curl first, then bootstrap the host with the GlueOps setup script.
# 'packages' is installed by cloud-init before 'runcmd' runs, so curl is guaranteed
# to be present; the runcmd string is executed via "sh -c", so the pipe works.
package_update: true
packages:
  - curl
runcmd:
  - curl -sL setup.glueops.dev | bash
`,
}
