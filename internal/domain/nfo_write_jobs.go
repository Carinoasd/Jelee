package domain

const JobNFOWrite = "nfo_write"

// Task data is owned by the job, independent of preparation expiry. Retrieving
// a task does not grant policy, filesystem or durable commit authorization.
type NFOWriteTask struct {
	JobID       string              `json:"-"`
	Sequence    int                 `json:"-"`
	Preparation NFOWritePreparation `json:"-"`
}

func (NFOWriteTask) String() string   { return "nfo write task (data redacted)" }
func (NFOWriteTask) GoString() string { return "nfo write task (data redacted)" }
