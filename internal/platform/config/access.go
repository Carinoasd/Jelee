package config

import (
	"errors"
	"net/http"
	"strconv"
)

// AccessConfig controls how direct ID requests for media the caller cannot see
// are answered (G48.3). 404 is the default so a response never confirms that
// an invisible item, source or image exists. 403 is an explicit opt-in.
type AccessConfig struct {
	// HiddenStatus is 404 or 403. Zero keeps the 404 default so configurations
	// built without this section retain the existing behaviour.
	HiddenStatus int `json:"hiddenStatus"`
}

func DefaultAccessConfig() AccessConfig { return AccessConfig{HiddenStatus: http.StatusNotFound} }

func (c AccessConfig) Validate() error {
	switch c.HiddenStatus {
	case 0, http.StatusNotFound, http.StatusForbidden:
		return nil
	}
	return errors.New("access hiddenStatus must be 404 or 403")
}

// HiddenContentStatus is the effective status for hidden media lookups.
func (c AccessConfig) HiddenContentStatus() int {
	if c.HiddenStatus == http.StatusForbidden {
		return http.StatusForbidden
	}
	return http.StatusNotFound
}

func (c *AccessConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_HIDDEN_CONTENT_STATUS"); ok {
		status, err := strconv.Atoi(value)
		if err != nil || (status != http.StatusNotFound && status != http.StatusForbidden) {
			return errors.New("invalid JELEE_HIDDEN_CONTENT_STATUS")
		}
		c.HiddenStatus = status
	}
	return nil
}
