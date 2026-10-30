package images

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var (
	_ app.ItemImageIngester   = (*Processor)(nil)
	_ app.ImageVariantCleaner = (*Processor)(nil)
)

// variantContentIndex is the optional part of the variant index that drops
// every entry derived from one original.
type variantContentIndex interface {
	DeleteImageVariantsOf(ctx context.Context, content [32]byte) (int64, error)
}

// IngestItemImage verifies one administrator upload and keeps it in the
// persistent store (G40.10, G40.11). The declared size must match the body
// exactly. Nothing about the upload is trusted: the format is sniffed, the
// header inspected and the memory estimate checked against the same budget
// as rendering, and the whole image is decoded once before it is stored.
// It runs under the processor's admission, deadline and shared work budget.
// Without a store it is ErrImageUnavailable.
func (p *Processor) IngestItemImage(ctx context.Context, body io.Reader, size int64) (content domain.ItemImageContent, err error) {
	if p == nil || ctx == nil || body == nil {
		return content, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return content, err
	}
	store := p.options.Store
	if store == nil {
		return content, domain.ErrImageUnavailable
	}
	// The upload is held in memory instead of the staging file a library
	// image uses, within the same per-image reservation (see render).
	limit := min(p.options.MaxSourceBytes, p.options.MaxImageBytes-(1<<20)-2*p.options.MaxOutputBytes)
	if size < 1 {
		return content, domain.ErrInvalid
	}
	if size > limit {
		return content, domain.ErrImageTooLarge
	}
	if err = p.admit(); err != nil {
		return content, err
	}
	defer p.release()
	operation, cancel := context.WithTimeout(ctx, p.options.Timeout)
	defer cancel()
	stopLifetime := context.AfterFunc(p.lifetime, cancel) //nolint:contextcheck // the processor lifetime also cancels the upload
	defer stopLifetime()
	defer func() {
		if err != nil {
			p.failed.Add(1)
		}
	}()

	release, err := p.acquireWork(operation, app.WorkIO)
	if err != nil {
		return content, err
	}
	data := make([]byte, size)
	_, readErr := io.ReadFull(contextImageReader{operation, body}, data)
	var extra [1]byte
	n, _ := io.ReadFull(body, extra[:])
	release()
	if err := operation.Err(); err != nil {
		return content, err
	}
	if readErr != nil || n != 0 {
		return content, domain.ErrInvalid
	}

	release, err = p.acquireWork(operation, app.WorkCPU)
	if err != nil {
		return content, err
	}
	inspected, err := p.verifyUpload(operation, data)
	release()
	if err != nil {
		return content, err
	}

	release, err = p.acquireWork(operation, app.WorkIO)
	if err != nil {
		return content, err
	}
	defer release()
	digest, stored, err := store.PutOriginal(operation, bytes.NewReader(data), size)
	if err != nil {
		if operation.Err() != nil {
			return content, operation.Err()
		}
		return content, domain.ErrImageUnavailable
	}
	if stored != size {
		return content, domain.ErrImageUnavailable
	}
	width, height := inspected.displaySize()
	p.completed.Add(1)
	return domain.ItemImageContent{SHA256: digest[:], Width: width, Height: height, Format: inspected.format, Bytes: stored,
		FetchedAt: time.Now().UTC()}, nil
}

// verifyUpload inspects and fully decodes the upload. A body that is not a
// supported, decodable image is ErrImageUnsupported.
func (p *Processor) verifyUpload(ctx context.Context, data []byte) (inspectedImage, error) {
	reader := bytes.NewReader(data)
	inspected, err := inspectImage(ctx, reader)
	if err != nil {
		return inspectedImage{}, uploadError(ctx, err)
	}
	if !itemImageFormats(inspected.format) {
		return inspectedImage{}, domain.ErrImageUnsupported
	}
	displayWidth, displayHeight := inspected.displaySize()
	width, height := targetSize(displayWidth, displayHeight, domain.ImageRequest{}, p.options.MaxOutputDimension)
	estimate, err := estimateImageBytes(inspected, width, height, int64(len(data)), p.options.MaxOutputBytes)
	if err != nil {
		return inspectedImage{}, uploadError(ctx, err)
	}
	if estimate > p.options.MaxImageBytes {
		return inspectedImage{}, domain.ErrImageTooLarge
	}
	p.mu.Lock()
	p.maxEstimate = max(p.maxEstimate, estimate)
	p.mu.Unlock()
	p.decodes.Add(1)
	if err := p.decodeUpload(ctx, reader, inspected); err != nil {
		return inspectedImage{}, err
	}
	if estimate >= min(int64(64<<20), p.options.MaxImageBytes/2) {
		p.reclaim()
	}
	return inspected, ctx.Err()
}

// decodeUpload keeps the decoded bitmap inside its own frame, like
// renderDecoded, so it is collectible before the original is stored.
func (p *Processor) decodeUpload(ctx context.Context, reader io.ReadSeeker, inspected inspectedImage) error {
	decoded, err := p.decode(ctx, reader, inspected)
	if err != nil {
		return uploadError(ctx, err)
	}
	if decoded.Bounds() != image.Rect(0, 0, inspected.width, inspected.height) {
		return domain.ErrImageUnsupported
	}
	return ctx.Err()
}

func itemImageFormats(format string) bool {
	switch format {
	case "jpeg", "png", "webp", "gif", "bmp", "tiff":
		return true
	}
	return false
}

// uploadError keeps cancellation and the memory refusal; any other failure
// to read the upload as an image is ErrImageUnsupported.
func uploadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, domain.ErrImageTooLarge) {
		return domain.ErrImageTooLarge
	}
	return domain.ErrImageUnsupported
}

// ClearItemImageVariants drops every variant Jelee generated from one
// original: the index entries, then the files of the live generation. The
// original itself, and any library file, is never touched (G40.11). Without
// a store there is nothing to clear.
func (p *Processor) ClearItemImageVariants(ctx context.Context, content [32]byte) error {
	if p == nil || ctx == nil {
		return domain.ErrInvalid
	}
	store := p.options.Store
	if store == nil {
		return nil
	}
	var indexErr error
	if index, ok := p.options.Index.(variantContentIndex); ok {
		if _, indexErr = index.DeleteImageVariantsOf(ctx, content); indexErr != nil {
			p.indexFailures.Add(1)
		}
	}
	_, err := store.RemoveVariantsOf(ctx, content)
	return errors.Join(indexErr, err)
}
