package images

import (
	"os"
	"syscall"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func privateImageObject(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return domain.ErrImageUnavailable
	}
	state, ok := info.Sys().(*syscall.Stat_t)
	if !ok || state.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 ||
		directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return domain.ErrImageUnavailable
	}
	return nil
}
