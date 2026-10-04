package subtitleocr

import (
	"bufio"
	"context"
	"errors"
	"image"
	"os"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// Runner is the isolated Tesseract mode (process.IsolatedToolRunner).
type Runner interface {
	Run(context.Context, process.ToolRequest) (process.Result, error)
}

// PictureRecognizer reads the text of one prepared subtitle picture.
type PictureRecognizer interface {
	Recognize(ctx context.Context, picture *image.Gray, languages []string) (string, error)
}

// Recognition failures. A picture Tesseract refuses is skipped; a busy,
// cancelled or broken runtime stops the job.
var (
	ErrPictureRejected = errors.New("subtitle_ocr_picture_rejected")
	ErrRecognizerBusy  = errors.New("subtitle_ocr_busy")
	ErrUnavailable     = errors.New("subtitle_ocr_unavailable")
)

// Recognizer runs the isolated Tesseract mode on one picture at a time. The
// picture is written as a binary PGM to a private file below directory,
// reopened read-only and passed as the verified stdin; the file is removed
// after the run. directory must be a private directory the caller owns.
type Recognizer struct {
	runner    Runner
	directory string
}

// NewRecognizer binds the runner to a private picture directory.
func NewRecognizer(runner Runner, directory string) (*Recognizer, error) {
	info, err := os.Lstat(directory)
	if !supportedPlatform || runner == nil || err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnavailable
	}
	return &Recognizer{runner: runner, directory: directory}, nil
}

// Recognize returns Tesseract's raw text for the picture.
func (r *Recognizer) Recognize(ctx context.Context, picture *image.Gray, languages []string) (string, error) {
	if picture == nil || len(languages) == 0 {
		return "", ErrPictureRejected
	}
	file, err := os.CreateTemp(r.directory, "picture-*.pgm")
	if err != nil {
		return "", ErrUnavailable
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	buffered := bufio.NewWriterSize(file, 64<<10)
	writeErr := bitmapsub.EncodePGM(buffered, picture)
	if writeErr == nil {
		writeErr = buffered.Flush()
	}
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return "", ErrUnavailable
	}
	input, err := os.Open(name) //nolint:gosec // G304: a private file this function just created
	if err != nil {
		return "", ErrUnavailable
	}
	defer func() { _ = input.Close() }()
	result, err := r.runner.Run(ctx, process.ToolRequest{Stdin: input, Extraction: sandbox.Extraction{Languages: languages}})
	switch {
	case err == nil:
		return string(result.Stdout), nil
	case ctx.Err() != nil:
		return "", ctx.Err()
	case errors.Is(err, process.ErrBusy):
		return "", ErrRecognizerBusy
	case errors.Is(err, process.ErrExit), errors.Is(err, process.ErrOutputLimit), errors.Is(err, process.ErrTimeout):
		return "", ErrPictureRejected
	default:
		return "", ErrUnavailable
	}
}
