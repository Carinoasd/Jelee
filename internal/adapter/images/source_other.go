//go:build !windows && !linux

package images

import (
	"os"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func privateImageObject(*os.File, bool) error { return domain.ErrImageUnavailable }
