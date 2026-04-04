package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id"`
}

type PendingOAuth struct {
	Verifier string `json:"verifier"`
	State    string `json:"state"`
}

type fileData struct {
	Tokens       *Tokens       `json:"tokens,omitempty"`
	PendingOAuth *PendingOAuth `json:"pending_oauth,omitempty"`
}

type TokenStore struct {
	path string
	mu   sync.Mutex
}

func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

func (s *TokenStore) LoadTokens() (*Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return nil, err
	}
	return data.Tokens, nil
}

func (s *TokenStore) SaveTokens(tokens *Tokens) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return err
	}
	data.Tokens = tokens
	return s.write(data)
}

func (s *TokenStore) ClearTokens() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return err
	}
	data.Tokens = nil
	return s.write(data)
}

func (s *TokenStore) LoadPending() (*PendingOAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return nil, err
	}
	return data.PendingOAuth, nil
}

func (s *TokenStore) SavePending(pending *PendingOAuth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return err
	}
	data.PendingOAuth = pending
	return s.write(data)
}

func (s *TokenStore) ClearPending() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return err
	}
	data.PendingOAuth = nil
	return s.write(data)
}

func (s *TokenStore) read() (fileData, error) {
	var data fileData
	content, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return data, nil
		}
		return data, err
	}
	err = json.Unmarshal(content, &data)
	return data, err
}

func (s *TokenStore) write(data fileData) error {
	dir := filepath.Dir(s.path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, content, 0o600)
}
