//go:build !unix

package runner

import "errors"

type secureJournalStore struct{}

func openSecureJournalStore(string, int) (*secureJournalStore, []byte, error) {
	return nil, nil, errors.New("runner journal requires a supported Unix platform")
}

func (*secureJournalStore) Write([]byte) error { return errors.New("runner journal is unavailable") }
func (*secureJournalStore) Close() error       { return nil }
