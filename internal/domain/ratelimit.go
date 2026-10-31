package domain

import "errors"

// ErrRateLimited refuses a request over the general per-client API rate
// limit (G08.7; 429 rate_limited). Client control rules use
// ErrClientRateLimited instead.
var ErrRateLimited = errors.New("api rate limited")
