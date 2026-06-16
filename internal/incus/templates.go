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

// ensureTemplatesDir creates the templates directory and seeds starter templates
// the first time it is empty.
func ensureTemplatesDir() (string, error) {
	dir := TemplatesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating templates dir: %w", err)
	}
	entries, _ := os.ReadDir(dir)
	hasYAML := false
	for _, e := range entries {
		if isYAML(e.Name()) {
			hasYAML = true
			break
		}
	}
	if !hasYAML {
		for name, body := range starterTemplates {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				return dir, fmt.Errorf("seeding template %s: %w", name, err)
			}
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
		out = append(out, Template{
			Name:    strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml"),
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
}
