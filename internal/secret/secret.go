package secret

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

var ErrNotFound = errors.New("secret not found")

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
	value, err := keyring.Get(s.service, ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	return value, err
}

func (s *KeyringStore) Put(ref, value string) error {
	return keyring.Set(s.service, ref, value)
}

func (s *KeyringStore) Delete(ref string) error {
	err := keyring.Delete(s.service, ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
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
		return "", fmt.Errorf("%w: %s", ErrNotFound, ref)
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
