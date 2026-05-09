package traefik_fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// cacheFile is the on-disk format for the last-known-good config.
type cacheFile struct {
	SavedAt  time.Time       `json:"saved_at"`
	Revision int             `json:"revision"`
	ETag     string          `json:"etag"`
	Response json.RawMessage `json:"response"`
}

// loadCache returns the most recently persisted response, or nil if no
// cache exists. A missing cache file is not an error.
func loadCache(path string) (*cacheFile, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cf cacheFile
	if err := json.Unmarshal(b, &cf); err != nil {
		return nil, fmt.Errorf("decode cache: %w", err)
	}
	if len(cf.Response) == 0 {
		return nil, nil
	}
	return &cf, nil
}

// saveCache atomically writes the cache file (write-temp + rename) so a
// crash mid-write cannot corrupt the cache.
func saveCache(path string, cr *configResponse, etag string) error {
	if path == "" || cr == nil {
		return nil
	}
	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(cr)
	if err != nil {
		return err
	}
	cf := cacheFile{
		SavedAt:  time.Now().UTC(),
		Revision: cr.Revision,
		ETag:     etag,
		Response: raw,
	}
	out, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cache-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
