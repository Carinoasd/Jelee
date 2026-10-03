package nfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

var ErrFileLock = errors.New("nfo_file_lock_failed")

type nfoFileLock struct {
	file  *os.File
	check func() bool
	once  sync.Once
	err   error
}

// lockNFOFile locks a persistent sidecar in an already-held parent directory.
// Never unlink the sidecar: a waiter could otherwise hold a different inode.
// The target NFO is not opened or modified by this operation. The caller owns
// the directory handle until the returned lock has been closed.
func lockNFOFile(ctx context.Context, directory *os.Root, filename string) (*nfoFileLock, error) {
	if ctx == nil || directory == nil || !IsNFOName(filename) || strings.ContainsFunc(filename, unicode.IsControl) {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(lockNameKey(filename)))
	name := ".jelee-nfo-" + hex.EncodeToString(digest[:]) + ".lock"
	file, err := directory.OpenFile(name, lockOpenFlags(), 0600)
	if err != nil {
		return nil, ErrFileLock
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !safeLockInfo(info) {
		return nil, ErrFileLock
	}
	check := func() bool {
		current, err := directory.Lstat(name)
		return err == nil && safeLockInfo(current) && os.SameFile(info, current)
	}
	if !check() {
		return nil, ErrFileLock
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		acquired, err := tryFileLock(file)
		if err != nil {
			return nil, ErrFileLock
		}
		if acquired {
			if err := ctx.Err(); err != nil {
				_ = unlockFile(file)
				return nil, err
			}
			if !check() {
				_ = unlockFile(file)
				return nil, ErrFileLock
			}
			keep = true
			return &nfoFileLock{file: file, check: check}, nil
		}
		timer.Reset(25 * time.Millisecond)
	}
}

func (l *nfoFileLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		unlockErr := unlockFile(l.file)
		closeErr := l.file.Close()
		if unlockErr != nil || closeErr != nil {
			l.err = ErrFileLock
		}
	})
	return l.err
}
