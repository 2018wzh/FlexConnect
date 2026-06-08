package file

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"flexconnect/internal/types"
)

type Data struct {
	Profiles         []types.Profile `json:"profiles"`
	CurrentProfileID string          `json:"current_profile_id"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() (Data, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var data Data
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	if len(b) == 0 {
		return data, nil
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return Data{}, err
	}
	return data, nil
}

func (s *Store) Save(data Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

