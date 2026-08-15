package clientstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const filename = "client.json"

// State contains non-secret, client-local runtime preferences. Keep this
// separate from provider configuration and the permanent chat archive.
type State struct {
	LastOutboundTarget string `json:"last_outbound_target,omitempty"`
}

type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

func Default() (*Store, error) {
	path, err := defaultPath(os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	return New(path), nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load deliberately treats every filesystem or decoding failure as empty
// state. A damaged preference must never prevent the interactive client from
// starting.
func (s *Store) Load() State {
	if s == nil || s.path == "" {
		return State{}
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return State{}
	}
	var state State
	if json.Unmarshal(data, &state) != nil {
		return State{}
	}
	return state
}

func (s *Store) Save(state State) error {
	if s == nil || s.path == "" {
		return errors.New("client state path is unavailable")
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("create client state directory")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return errors.New("secure client state directory")
	}
	temporary, err := os.CreateTemp(directory, ".client-*.tmp")
	if err != nil {
		return errors.New("create temporary client state")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		encoder := json.NewEncoder(temporary)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(state)
	}
	if syncErr := temporary.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("write client state")
	}
	if err = os.Rename(temporaryPath, s.path); err != nil {
		return errors.New("replace client state")
	}
	if err = os.Chmod(s.path, 0600); err != nil {
		return errors.New("secure client state")
	}
	if directoryFile, openErr := os.Open(directory); openErr == nil {
		syncErr := directoryFile.Sync()
		closeErr := directoryFile.Close()
		if syncErr != nil || closeErr != nil {
			return errors.New("sync client state directory")
		}
	}
	return nil
}

func defaultPath(getenv func(string) string, homeDir func() (string, error)) (string, error) {
	base := getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := homeDir()
		if err != nil || home == "" {
			return "", errors.New("locate home directory for client state")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "streamchat", filename), nil
}
