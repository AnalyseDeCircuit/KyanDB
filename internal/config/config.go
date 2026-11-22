// Package config persists non-sensitive KyanDB connection profiles to JSON.
// Credentials must never be written here.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

type diskFormat struct {
	Version  int              `json:"version"`
	Profiles []domain.Profile `json:"profiles"`
}

// Store loads and atomically saves non-sensitive connection profiles.
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore opens a profile store at path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the configured profiles file path.
func (s *Store) Path() string { return s.path }

// DefaultPath returns the OS-standard per-user profile file path.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kyandb", "profiles.json"), nil
}

// Load returns all saved profiles. A missing file returns an empty slice and
// nil error so first-run launches start cleanly with a true empty state.
func (s *Store) Load() ([]domain.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var doc diskFormat
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc.Profiles, nil
}

// Save writes profiles atomically using a temporary sibling file and rename.
func (s *Store) Save(profiles []domain.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	doc := diskFormat{Version: 1, Profiles: profiles}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
