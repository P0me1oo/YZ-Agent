package cert

import (
	"context"
	"path/filepath"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/caddyserver/certmagic"
)

type privateStorage struct {
	certmagic.Storage
	root string
}

func (s *privateStorage) Store(ctx context.Context, key string, value []byte) error {
	release := privacy.LockWrite()
	defer release()
	data, err := privacy.Protect(filepath.Join(s.root, key), value)
	if err != nil {
		return err
	}
	return s.Storage.Store(ctx, key, data)
}

func (s *privateStorage) Load(ctx context.Context, key string) ([]byte, error) {
	data, err := s.Storage.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	return privacy.OpenFor(filepath.Join(s.root, key), data)
}
