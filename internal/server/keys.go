package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// KeyStore holds the TMDB API key: from TMDB_API_KEY, or from a file in
// DATA_DIR written once by the setup page (mode 0600).
type KeyStore struct {
	mu      sync.RWMutex
	key     string
	fromEnv bool
	path    string
}

func LoadKeyStore(envKey, dataDir string, log *slog.Logger) (*KeyStore, error) {
	ks := &KeyStore{path: filepath.Join(dataDir, "tmdb_api_key")}
	if envKey != "" {
		ks.key, ks.fromEnv = envKey, true
		return ks, nil
	}
	b, err := os.ReadFile(ks.path)
	if errors.Is(err, fs.ErrNotExist) {
		return ks, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read API key file: %w", err)
	}
	if fi, err := os.Stat(ks.path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		log.Warn("API key file is readable by others; fixing mode to 0600")
		_ = os.Chmod(ks.path, 0o600)
	}
	ks.key = strings.TrimSpace(string(b))
	return ks, nil
}

func (k *KeyStore) Get() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.key
}

func (k *KeyStore) FromEnv() bool { return k.fromEnv }

// Set writes the key atomically with mode 0600.
func (k *KeyStore) Set(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	tmp, err := os.CreateTemp(filepath.Dir(k.path), ".tmdb_api_key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(key + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), k.path); err != nil {
		return err
	}
	k.key = key
	return nil
}
