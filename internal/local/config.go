package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/sam-jeston/braid/internal/api"
)

// Profile is one team membership on one server.
type Profile struct {
	ID          string `json:"id"`
	ServerURL   string `json:"server_url"`
	ServerID    string `json:"server_id"`
	TeamID      string `json:"team_id"`
	TeamName    string `json:"team_name"`
	MemberID    string `json:"member_id"`
	DeviceID    string `json:"device_id"`
	DisplayName string `json:"display_name"`
	// RepoOverrides maps a team repository's name to a local checkout this
	// member wants agents to read as-is instead of Braid's managed clone.
	RepoOverrides map[string]string `json:"repo_overrides,omitempty"`
	// RepoOverride is the single-repository setting from before teams could
	// have several. It applies to the team's first repository until replaced.
	RepoOverride string `json:"repo_override,omitempty"`
}

// overrideFor returns the local checkout to use for the team's i-th repository, if any.
func (p Profile) overrideFor(repos []api.RepoRef, i int) string {
	if dir := p.RepoOverrides[repos[i].Name]; dir != "" {
		return dir
	}
	if i == 0 {
		return p.RepoOverride
	}
	return ""
}

type Config struct {
	Profiles      []Profile `json:"profiles"`
	Active        string    `json:"active"`
	Agent         string    `json:"agent"`
	AgentModel    string    `json:"agent_model"`
	JiraEmail     string    `json:"jira_email"`
	ExportDir     string    `json:"export_dir"`
	SecretBackend string    `json:"secret_backend"`
	// AgentHost is where agents and git run: "local" or "wsl:<distro>".
	// Empty until chosen, which lets Windows pick WSL automatically.
	AgentHost string `json:"agent_host,omitempty"`
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

func (c *Config) profile(id string) *Profile {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			return &c.Profiles[i]
		}
	}
	return nil
}

func writeFileAtomic(path string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Secrets stores device private keys and tracker credentials. The OS keychain
// is used when available; otherwise a 0600 file in the Braid home directory.
type Secrets interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	Backend() string
}

var errNoSecret = errors.New("secret not found")

const keyringService = "dev.braid.desktop"

type keyringSecrets struct{}

func (keyringSecrets) Get(k string) (string, error) {
	v, err := keyring.Get(keyringService, k)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNoSecret
	}
	return v, err
}
func (keyringSecrets) Set(k, v string) error { return keyring.Set(keyringService, k, v) }
func (keyringSecrets) Delete(k string) error {
	err := keyring.Delete(keyringService, k)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
func (keyringSecrets) Backend() string { return "keychain" }

type fileSecrets struct {
	path string
	mu   sync.Mutex
}

func (f *fileSecrets) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (f *fileSecrets) Get(k string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[k]
	if !ok {
		return "", errNoSecret
	}
	return v, nil
}

func (f *fileSecrets) Set(k, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[k] = v
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeFileAtomic(f.path, b, 0o600)
}

func (f *fileSecrets) Delete(k string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, k)
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeFileAtomic(f.path, b, 0o600)
}

func (f *fileSecrets) Backend() string { return "file" }

// openSecrets picks a backend once and sticks with it (recorded in config),
// so secrets are never split across stores.
func openSecrets(home, preferred string) Secrets {
	file := &fileSecrets{path: filepath.Join(home, "secrets.json")}
	switch preferred {
	case "file":
		return file
	case "keychain":
		return keyringSecrets{}
	}
	if os.Getenv("BRAID_SECRETS") == "file" || !keyringWorks() {
		return file
	}
	return keyringSecrets{}
}

func keyringWorks() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok := make(chan bool, 1)
	go func() {
		k := "probe"
		if err := keyring.Set(keyringService, k, "1"); err != nil {
			ok <- false
			return
		}
		v, err := keyring.Get(keyringService, k)
		_ = keyring.Delete(keyringService, k)
		ok <- err == nil && v == "1"
	}()
	select {
	case v := <-ok:
		return v
	case <-ctx.Done():
		return false
	}
}
