package process

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type linuxChild struct{ process *os.Process }

func allowedProductionName(path string) bool { return filepath.Base(path) == "ffprobe" }

func executableAllowed(_ string, info os.FileInfo) bool { return info.Mode()&0111 != 0 }
func platformEnvironment(env []string) []string         { return env }

func validInput(file *os.File) bool {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return false
	}
	_, err = file.Seek(0, io.SeekCurrent)
	return err == nil
}

func startChild(path string, args []string, dir string, env []string, stdin, stdout, stderr *os.File) (child, error) {
	p, err := os.StartProcess(path, append([]string{path}, args...), &os.ProcAttr{Dir: dir, Env: env, Files: []*os.File{stdin, stdout, stderr}, Sys: &syscall.SysProcAttr{Setpgid: true}})
	if err != nil {
		return nil, err
	}
	return &linuxChild{process: p}, nil
}

func (p *linuxChild) ready() error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, p.process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
func (p *linuxChild) kill() error {
	err := unix.Kill(-p.process.Pid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
func (p *linuxChild) reap() (int, error) {
	state, err := p.process.Wait()
	if err != nil {
		return -1, err
	}
	return state.ExitCode(), nil
}
func (p *linuxChild) close() error { return nil } // Wait releases the process handle/pidfd.
