package config

import (
	"errors"
	"fmt"
	"strconv"
)

type AccountsConfig struct {
	PasswordMemoryKiB   int `json:"passwordMemoryKiB"`
	PasswordIterations  int `json:"passwordIterations"`
	PasswordParallelism int `json:"passwordParallelism"`
	PasswordConcurrency int `json:"passwordConcurrency"`
	LoginIPLimit        int `json:"loginIPLimit"`
	LoginUserLimit      int `json:"loginUserLimit"`
	LoginWindowSeconds  int `json:"loginWindowSeconds"`
	LoginMaxEntries     int `json:"loginMaxEntries"`
	LockAfter           int `json:"lockAfter"`
	LockSeconds         int `json:"lockSeconds"`
	MaxSessions         int `json:"maxSessions"`
	SessionHours        int `json:"sessionHours"`
}

func DefaultAccountsConfig() AccountsConfig {
	return AccountsConfig{PasswordMemoryKiB: 65536, PasswordIterations: 3, PasswordParallelism: 2, PasswordConcurrency: 2, LoginIPLimit: 60, LoginUserLimit: 10, LoginWindowSeconds: 60, LoginMaxEntries: 10000, LockAfter: 5, LockSeconds: 900, MaxSessions: 8, SessionHours: 24}
}

func (c AccountsConfig) Validate() error {
	if c.PasswordMemoryKiB < 19456 || c.PasswordMemoryKiB > 131072 || c.PasswordIterations < 2 || c.PasswordIterations > 6 || c.PasswordParallelism < 1 || c.PasswordParallelism > 4 || c.PasswordConcurrency < 1 || c.PasswordConcurrency > 8 {
		return errors.New("password work factors or concurrency are outside supported limits")
	}
	if c.LoginIPLimit < 1 || c.LoginIPLimit > 10000 || c.LoginUserLimit < 1 || c.LoginUserLimit > 1000 || c.LoginWindowSeconds < 1 || c.LoginWindowSeconds > 3600 || c.LoginMaxEntries < 2 || c.LoginMaxEntries > 100000 {
		return errors.New("login throttling is outside supported limits")
	}
	if c.LockAfter < 3 || c.LockAfter > 100 || c.LockSeconds < 60 || c.LockSeconds > 86400 || c.MaxSessions < 1 || c.MaxSessions > 100 || c.SessionHours < 1 || c.SessionHours > 720 {
		return errors.New("account lock or session policy is outside supported limits")
	}
	return nil
}

func (c *AccountsConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for key, target := range map[string]*int{
		"JELEE_PASSWORD_MEMORY_KIB":  &c.PasswordMemoryKiB,
		"JELEE_PASSWORD_ITERATIONS":  &c.PasswordIterations,
		"JELEE_PASSWORD_PARALLELISM": &c.PasswordParallelism,
		"JELEE_PASSWORD_CONCURRENCY": &c.PasswordConcurrency,
		"JELEE_LOGIN_IP_LIMIT":       &c.LoginIPLimit,
		"JELEE_LOGIN_USER_LIMIT":     &c.LoginUserLimit,
		"JELEE_LOGIN_WINDOW_SECONDS": &c.LoginWindowSeconds,
		"JELEE_LOGIN_MAX_ENTRIES":    &c.LoginMaxEntries,
		"JELEE_LOGIN_LOCK_AFTER":     &c.LockAfter,
		"JELEE_LOGIN_LOCK_SECONDS":   &c.LockSeconds,
		"JELEE_MAX_SESSIONS":         &c.MaxSessions,
		"JELEE_SESSION_HOURS":        &c.SessionHours,
	} {
		if value, ok := lookup(key); ok {
			number, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = number
		}
	}
	return nil
}
