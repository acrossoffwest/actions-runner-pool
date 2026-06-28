package slots

import (
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlSlot is the YAML deserialization target. It includes uid (used by the
// provisioner) which is not stored in the DB; all other fields map to Slot.
type yamlSlot struct {
	ID           string `yaml:"id"`
	OSUser       string `yaml:"os_user"`
	UID          int    `yaml:"uid"`
	DockerHost   string `yaml:"docker_host"`
	Network      string `yaml:"network"`
	BaseURL      string `yaml:"base_url"`
	InternalAddr string `yaml:"internal_addr"`
	CPULimit     string `yaml:"cpu_limit"`
	MemLimit     string `yaml:"mem_limit"`
	MaxRunners   int    `yaml:"max_runners"`
	AdminToken   string `yaml:"admin_token"`
}

type yamlFile struct {
	Slots []yamlSlot `yaml:"slots"`
}

// ParseFile reads and validates a slots.yaml from r.
// Returns an error if any required field is missing, any id is duplicated, or
// any docker_host scheme is not "unix" or "tcp".
func ParseFile(r io.Reader) ([]Slot, error) {
	var f yamlFile
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("slots: yaml decode: %w", err)
	}
	if len(f.Slots) == 0 {
		return nil, fmt.Errorf("slots: yaml contains no slots")
	}

	seen := make(map[string]struct{}, len(f.Slots))
	out := make([]Slot, 0, len(f.Slots))

	for i, ys := range f.Slots {
		if err := validateYAMLSlot(i, ys); err != nil {
			return nil, err
		}
		if _, dup := seen[ys.ID]; dup {
			return nil, fmt.Errorf("slots: duplicate slot id %q", ys.ID)
		}
		seen[ys.ID] = struct{}{}

		mr := ys.MaxRunners
		if mr == 0 {
			mr = 4 // match DB default
		}
		out = append(out, Slot{
			ID:           ys.ID,
			OSUser:       ys.OSUser,
			DockerHost:   ys.DockerHost,
			Network:      ys.Network,
			BaseURL:      ys.BaseURL,
			InternalAddr: ys.InternalAddr,
			CPULimit:     ys.CPULimit,
			MemLimit:     ys.MemLimit,
			MaxRunners:   mr,
			AdminToken:   ys.AdminToken,
			Status:       "free",
		})
	}
	return out, nil
}

func validateYAMLSlot(i int, ys yamlSlot) error {
	pos := fmt.Sprintf("slots[%d]", i)
	if ys.ID == "" {
		return fmt.Errorf("slots: %s: id is required", pos)
	}
	if ys.OSUser == "" {
		return fmt.Errorf("slots: %s (id=%q): os_user is required", pos, ys.ID)
	}
	if ys.DockerHost == "" {
		return fmt.Errorf("slots: %s (id=%q): docker_host is required", pos, ys.ID)
	}
	if !strings.HasPrefix(ys.DockerHost, "unix://") && !strings.HasPrefix(ys.DockerHost, "tcp://") {
		return fmt.Errorf("slots: %s (id=%q): docker_host must start with unix:// or tcp://, got %q", pos, ys.ID, ys.DockerHost)
	}
	if ys.Network == "" {
		return fmt.Errorf("slots: %s (id=%q): network is required", pos, ys.ID)
	}
	if ys.BaseURL == "" {
		return fmt.Errorf("slots: %s (id=%q): base_url is required", pos, ys.ID)
	}
	if ys.InternalAddr == "" {
		return fmt.Errorf("slots: %s (id=%q): internal_addr is required", pos, ys.ID)
	}
	return nil
}

// LoadRegistry parses r as slots.yaml and upserts every slot into st.
// Idempotent: calling it twice with the same data calls UpsertSlot twice; the
// store is responsible for making upserts idempotent at the DB level.
func LoadRegistry(r io.Reader, st Store) error {
	ss, err := ParseFile(r)
	if err != nil {
		return err
	}
	for _, s := range ss {
		if err := st.UpsertSlot(s); err != nil {
			return fmt.Errorf("slots: upsert %q: %w", s.ID, err)
		}
	}
	return nil
}
