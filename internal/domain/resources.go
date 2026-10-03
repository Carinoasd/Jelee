package domain

import "errors"

var ErrResourceBusy = errors.New("shared resource queue is full")
