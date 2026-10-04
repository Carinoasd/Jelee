package config

import (
	"errors"
	"io"
	"os"
	"strings"
)

func loadTMDBKey(lookup func(string) (string, bool)) (string, error) {
	key, present := lookup("TMDB_API_KEY")
	path, filePresent := lookup("TMDB_API_KEY_FILE")
	if present && filePresent {
		return "", errors.New("set only one TMDB credential source")
	}
	if filePresent {
		if path == "" {
			return "", errors.New("cannot read TMDB credential file")
		}
		f, err := os.Open(path) //nolint:gosec // G304: the operator names the secret file
		if err != nil {
			return "", errors.New("cannot read TMDB credential file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil || len(data) > 4096 {
			return "", errors.New("invalid TMDB credential file")
		}
		key = strings.TrimSpace(string(data))
	}
	if (present || filePresent) && !validTMDBKey(key) {
		return "", errors.New("invalid TMDB_API_KEY")
	}
	return key, nil
}

func validTMDBKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	for _, ch := range key {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}
