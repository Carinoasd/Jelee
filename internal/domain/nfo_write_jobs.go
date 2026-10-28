package domain

import "errors"

const JobNFOWrite = "nfo_write"

// ErrNFOWriteDisabled refuses an NFO write job for a library that is not
// read-only with writes enabled (409 nfo_write_disabled).
var ErrNFOWriteDisabled = errors.New("nfo_write_disabled")

// Task data is owned by the job, independent of preparation expiry. Retrieving
// a task does not grant policy, filesystem or durable commit authorization.
type NFOWriteTask struct {
	JobID       string              `json:"-"`
	Sequence    int                 `json:"-"`
	Preparation NFOWritePreparation `json:"-"`
}

func (NFOWriteTask) String() string   { return "nfo write task (data redacted)" }
func (NFOWriteTask) GoString() string { return "nfo write task (data redacted)" }
