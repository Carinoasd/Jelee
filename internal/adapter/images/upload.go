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
	limit := p.ItemImageLimit()
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
	return p.keepItemImage(operation, store, data)
}

// ItemImageLimit is the largest image IngestItemImage and
// IngestFetchedItemImage accept: the source limit, within one image's
// working reservation.
func (p *Processor) ItemImageLimit() int64 {
	return min(p.options.MaxSourceBytes, p.options.MaxImageBytes-(1<<20)-2*p.options.MaxOutputBytes)
}

// IngestFetchedItemImage verifies and stores the bytes of one fetched remote
// image (G40.9) exactly like an upload: the format is sniffed, the header
// and memory estimate checked and the whole image decoded once before it is
// kept in the persistent store. The caller already holds the bytes, so only
// verification and storage run under the processor's admission; a full
// processor answers ErrImageBusy at once. Without a store it is
// ErrImageUnavailable.
func (p *Processor) IngestFetchedItemImage(ctx context.Context, data []byte) (content domain.ItemImageContent, err error) {
	if p == nil || ctx == nil {
		return content, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return content, err
	}
	store := p.options.Store
	if store == nil {
		return content, domain.ErrImageUnavailable
	}
	if len(data) == 0 {
		return content, domain.ErrImageUnsupported
	}
	if int64(len(data)) > p.ItemImageLimit() {
		return content, domain.ErrImageTooLarge
	}
	if err = p.admit(); err != nil {
		return content, err
	}
	defer p.release()
	operation, cancel := context.WithTimeout(ctx, p.options.Timeout)
	defer cancel()
	stopLifetime := context.AfterFunc(p.lifetime, cancel) //nolint:contextcheck // the processor lifetime also cancels the fetch
	defer stopLifetime()
	defer func() {
		if err != nil {
			p.failed.Add(1)
		}
	}()
	return p.keepItemImage(operation, store, data)
}

// keepItemImage verifies data and keeps it in store, under the caller's
// admission and deadline.
func (p *Processor) keepItemImage(operation context.Context, store *Store, data []byte) (domain.ItemImageContent, error) {
	var content domain.ItemImageContent
	release, err := p.acquireWork(operation, app.WorkCPU)
	if err != nil {
		return content, err
	}
	inspected, colors, err := p.verifyUpload(operation, data)
	release()
	if err != nil {
		return content, err
	}

	release, err = p.acquireWork(operation, app.WorkIO)
	if err != nil {
		return content, err
	}
	defer release()
	size := int64(len(data))
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
		AverageColor: &colors.average, DominantColor: &colors.dominant, FetchedAt: time.Now().UTC()}, nil
}

// sampledColors are the G40.7 colors of one decoded image.
type sampledColors struct{ average, dominant int }

// verifyUpload inspects and fully decodes the upload, sampling its colors
// from the decoded bitmap. A body that is not a supported, decodable image
// is ErrImageUnsupported.
func (p *Processor) verifyUpload(ctx context.Context, data []byte) (inspectedImage, sampledColors, error) {
	inspected, colors, err := p.verifyImage(ctx, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return inspectedImage{}, sampledColors{}, uploadError(ctx, err)
	}
	return inspected, colors, nil
}

// verifyImage inspects, budget-checks and fully decodes one original of
// size bytes, and samples its G40.7 colors. The only bitmap is the decoded
// one, within the same estimate as a full-size render.
func (p *Processor) verifyImage(ctx context.Context, reader io.ReadSeeker, size int64) (inspectedImage, sampledColors, error) {
	inspected, err := inspectImage(ctx, reader)
	if err != nil {
		return inspectedImage{}, sampledColors{}, err
	}
	if !itemImageFormats(inspected.format) {
		return inspectedImage{}, sampledColors{}, domain.ErrImageUnsupported
	}
	displayWidth, displayHeight := inspected.displaySize()
	width, height := targetSize(displayWidth, displayHeight, domain.ImageRequest{}, p.options.MaxOutputDimension)
	estimate, err := estimateImageBytes(inspected, width, height, size, p.options.MaxOutputBytes)
	if err != nil {
		return inspectedImage{}, sampledColors{}, err
	}
	if estimate > p.options.MaxImageBytes {
		return inspectedImage{}, sampledColors{}, domain.ErrImageTooLarge
	}
	p.mu.Lock()
	p.maxEstimate = max(p.maxEstimate, estimate)
	p.mu.Unlock()
	p.decodes.Add(1)
	colors, err := p.decodeColors(ctx, reader, inspected)
	if estimate >= min(int64(64<<20), p.options.MaxImageBytes/2) {
		p.reclaim()
	}
	if err != nil {
		return inspectedImage{}, sampledColors{}, err
	}
	return inspected, colors, ctx.Err()
}

// decodeColors keeps the decoded bitmap inside its own frame, like
// renderDecoded, so it is collectible before the original is stored.
func (p *Processor) decodeColors(ctx context.Context, reader io.ReadSeeker, inspected inspectedImage) (sampledColors, error) {
	decoded, err := p.decode(ctx, reader, inspected)
	if err != nil {
		return sampledColors{}, err
	}
	if decoded.Bounds() != image.Rect(0, 0, inspected.width, inspected.height) {
		return sampledColors{}, domain.ErrImageUnsupported
	}
	average, dominant, err := imageColors(ctx, decoded)
	if err != nil {
		return sampledColors{}, err
	}
	return sampledColors{average: average, dominant: dominant}, ctx.Err()
}

// DescribeStoredImage reads one original from the persistent store, checks
// it like an upload (format sniffing, header preflight, memory estimate, one
// full decode) and returns its G40.7 metadata: display size, format, byte
// size and the sampled average and dominant colors. FetchedAt is left zero
// for the caller, which knows when the bytes were obtained. Embedded
// artwork extraction and image refresh call it after storing the bytes. It
// runs under the processor's admission and shared work budget and fails
// fast with ErrImageBusy when every slot is taken.
func (p *Processor) DescribeStoredImage(ctx context.Context, digest [32]byte) (content domain.ItemImageContent, err error) {
	if p == nil || ctx == nil {
		return content, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return content, err
	}
	store := p.options.Store
	if store == nil {
		return content, domain.ErrImageUnavailable
	}
	if err = p.admit(); err != nil {
		return content, err
	}
	defer p.release()
	operation, cancel := context.WithTimeout(ctx, p.options.Timeout)
	defer cancel()
	stopLifetime := context.AfterFunc(p.lifetime, cancel) //nolint:contextcheck // the processor lifetime also cancels the read
	defer stopLifetime()
	defer func() {
		if err != nil {
			p.failed.Add(1)
		}
	}()
	release, err := p.acquireWork(operation, app.WorkCPU)
	if err != nil {
		return content, err
	}
	defer release()
	original := &storedOriginal{store: store, digest: digest}
	defer func() { _ = original.Close() }()
	limit := min(p.options.MaxSourceBytes, p.options.MaxImageBytes-(1<<20)-2*p.options.MaxOutputBytes)
	reader, size, err := original.Open(operation, limit)
	if err != nil {
		return content, imageError(operation, err)
	}
	inspected, colors, err := p.verifyImage(operation, reader, size)
	if err != nil {
		return content, imageError(operation, err)
	}
	width, height := inspected.displaySize()
	p.completed.Add(1)
	return domain.ItemImageContent{SHA256: append([]byte(nil), digest[:]...), Width: width, Height: height, Format: inspected.format,
		Bytes: size, AverageColor: &colors.average, DominantColor: &colors.dominant}, nil
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
