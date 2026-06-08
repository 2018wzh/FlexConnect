package secret

import (
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

type Store interface {
	Get(ref string) (string, error)
	Put(ref, value string) error
	Delete(ref string) error
}

type KeyringStore struct {
	service string
}

func NewKeyringStore(service string) *KeyringStore {
	return &KeyringStore{service: service}
}

func (s *KeyringStore) Get(ref string) (string, error) {
	return keyring.Get(s.service, ref)
}

func (s *KeyringStore) Put(ref, value string) error {
	return keyring.Set(s.service, ref, value)
}

func (s *KeyringStore) Delete(ref string) error {
	return keyring.Delete(s.service, ref)
}

type MemoryStore struct {
	mu   sync.Mutex
	data map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: map[string]string{}}
}

func (s *MemoryStore) Get(ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[ref]
	if !ok {
		return "", fmt.Errorf("secret not found: %s", ref)
	}
	return v, nil
}

func (s *MemoryStore) Put(ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[ref] = value
	return nil
}

func (s *MemoryStore) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, ref)
	return nil
}
