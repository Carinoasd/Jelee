package images

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.ItemImageFetcher = (*ItemImageRefresher)(nil)

// ItemImageRefresher fetches the remote artwork of item_images rows for the
// image_refresh job (G40.9). Every request goes through the RemoteFetcher
// (controlled transport, address policy, per-host spacing, retries, size
// limit) and every body is verified like an upload before the persistent
// store keeps it, so a remote source can never place bytes that are not a
// decodable image within the processing budget.
type ItemImageRefresher struct {
	fetcher   *RemoteFetcher
	processor *Processor
	limit     int64
	busyWait  time.Duration
}

// NewItemImageRefresher requires a processor with a persistent store; the
// fetcher's own body limit should not exceed processor.ItemImageLimit (a
// larger body is refused after the download).
func NewItemImageRefresher(fetcher *RemoteFetcher, processor *Processor) (*ItemImageRefresher, error) {
	if fetcher == nil || processor == nil {
		return nil, domain.ErrInvalid
	}
	if processor.options.Store == nil {
		return nil, domain.ErrImageUnavailable
	}
	return &ItemImageRefresher{fetcher: fetcher, processor: processor, limit: processor.ItemImageLimit(), busyWait: 250 * time.Millisecond}, nil
}

// FetchItemImage implements app.ItemImageFetcher. Artwork on the official
// TMDB image host follows the stricter TMDB rule (canonical URL, no
// redirects); any other HTTPS URL follows the NFO rule.
func (r *ItemImageRefresher) FetchItemImage(ctx context.Context, url string) (domain.ItemImageContent, error) {
	if r == nil || ctx == nil {
		return domain.ItemImageContent{}, domain.ErrInvalid
	}
	source := FetchSourceNFO
	if strings.HasPrefix(url, "https://"+tmdbImageHost+"/") {
		source = FetchSourceTMDB
	}
	sink := &verifiedImageSink{processor: r.processor, limit: r.limit, busyWait: r.busyWait}
	result, err := r.fetcher.Fetch(ctx, FetchRequest{Source: source, URL: url}, sink)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.ItemImageContent{}, ctxErr
		}
		return domain.ItemImageContent{}, fetchFailure(err, sink.err)
	}
	if sink.content == nil || hex.EncodeToString(sink.content.SHA256) != result.Digest || sink.content.Bytes != result.Size {
		return domain.ItemImageContent{}, &domain.ImageFetchFailure{Reason: domain.ImageFetchStoreFailed}
	}
	return *sink.content, nil
}

// verifiedImageSink reads one response body to the end, then hands the bytes
// to the processor's upload verification and the store. The processor slot
// is taken only after the network read, so a slow source never holds it.
type verifiedImageSink struct {
	processor *Processor
	limit     int64
	busyWait  time.Duration
	content   *domain.ItemImageContent
	err       error
}

func (s *verifiedImageSink) Put(ctx context.Context, body io.Reader) (string, int64, error) {
	s.content, s.err = nil, nil
	data, err := io.ReadAll(io.LimitReader(body, s.limit+1))
	if err != nil {
		return "", 0, err
	}
	if int64(len(data)) > s.limit {
		s.err = domain.ErrImageTooLarge
		return "", 0, s.err
	}
	busy := false
	for {
		content, err := s.processor.IngestFetchedItemImage(ctx, data)
		if errors.Is(err, domain.ErrImageBusy) {
			// Rendering for viewers owns the slots; wait for one instead of
			// failing the row, within the attempt deadline.
			busy = true
			if waitFetch(ctx, s.busyWait) == nil {
				continue
			}
			err = ctx.Err()
		}
		if err != nil {
			s.err = err
			if busy && ctx.Err() != nil {
				s.err = domain.ErrImageBusy
			}
			return "", 0, s.err
		}
		s.content = &content
		return hex.EncodeToString(content.SHA256), content.Bytes, nil
	}
}

// fetchFailure maps a fetch error onto a stored failure reason. Verification
// refusals of the sink decide when the fetcher only saw a store or deadline
// failure.
func fetchFailure(err, sinkErr error) *domain.ImageFetchFailure {
	status := 0
	var fetchErr *FetchError
	if errors.As(err, &fetchErr) {
		status = fetchErr.Status
	}
	if status < 100 || status > 599 {
		status = 0
	}
	if sinkErr != nil && (errors.Is(err, ErrFetchStore) || errors.Is(err, ErrFetchTimeout)) {
		switch {
		case errors.Is(sinkErr, domain.ErrImageUnsupported):
			return &domain.ImageFetchFailure{Reason: domain.ImageFetchUnsupported, Status: status, Permanent: true}
		case errors.Is(sinkErr, domain.ErrImageTooLarge):
			return &domain.ImageFetchFailure{Reason: domain.ImageFetchTooLarge, Status: status, Permanent: true}
		case errors.Is(sinkErr, domain.ErrImageBusy):
			return &domain.ImageFetchFailure{Reason: domain.ImageFetchBusy, Status: status}
		case errors.Is(sinkErr, context.Canceled), errors.Is(sinkErr, context.DeadlineExceeded):
			return &domain.ImageFetchFailure{Reason: domain.ImageFetchTimeout, Status: status}
		}
		return &domain.ImageFetchFailure{Reason: domain.ImageFetchStoreFailed, Status: status}
	}
	for _, m := range fetchFailureReasons {
		if errors.Is(err, m.err) {
			return &domain.ImageFetchFailure{Reason: m.reason, Status: status, Permanent: m.permanent}
		}
	}
	return &domain.ImageFetchFailure{Reason: domain.ImageFetchUnavailable, Status: status}
}

var fetchFailureReasons = []struct {
	err       error
	reason    string
	permanent bool
}{
	{ErrFetchLocalOnly, domain.ImageFetchLocalOnly, false},
	{ErrFetchInvalidURL, domain.ImageFetchURLRejected, true},
	{ErrFetchDenied, domain.ImageFetchTargetDenied, true},
	{ErrFetchRedirect, domain.ImageFetchRedirectDenied, true},
	{ErrFetchTooLarge, domain.ImageFetchTooLarge, true},
	{ErrFetchContentType, domain.ImageFetchContentTypeRejected, true},
	{ErrFetchFormat, domain.ImageFetchFormatRejected, true},
	{ErrFetchNotFound, domain.ImageFetchNotFound, true},
	{ErrFetchRejected, domain.ImageFetchStatusRejected, true},
	{ErrFetchRateLimited, domain.ImageFetchRateLimited, false},
	{ErrFetchUnavailable, domain.ImageFetchUnavailable, false},
	{ErrFetchTimeout, domain.ImageFetchTimeout, false},
	{ErrFetchStore, domain.ImageFetchStoreFailed, false},
}
