package nfo

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// SummaryReader exposes safe full-byte observations and bounded validation
// summaries. It does not persist data, select parsers from database fields, or
// enable any worker/API. The caller supplies its concurrency and context budget.
type SummaryReader struct{ identity domain.NFOIdentity }

var _ app.NFOReader = (*SummaryReader)(nil)
var _ app.NFOReadSource = (*summarySource)(nil)

// NewSummaryReader accepts only the source byte budget. Parser, projection and
// fingerprint versions are compiled constants, never caller-selected identity.
func NewSummaryReader(maxSourceBytes int64) (*SummaryReader, error) {
	if maxSourceBytes < 1 || maxSourceBytes > MaxAllowedBytes {
		return nil, domain.ErrInvalid
	}
	identity := domain.NFOIdentity{
		ParserVersion: domain.NFOParserVersion, SummarySchemaVersion: domain.NFOSummarySchemaVersion,
		FingerprintVersion: SourceFingerprintVersion, MaxSourceBytes: maxSourceBytes,
	}
	if err := domain.ValidateNFOIdentity(identity); err != nil {
		return nil, domain.ErrNFOReaderUnavailable
	}
	return &SummaryReader{identity: identity}, nil
}

func (r *SummaryReader) Identity() domain.NFOIdentity {
	if r == nil {
		return domain.NFOIdentity{}
	}
	return r.identity
}

// Read only reads and hashes. Invalid XML still yields a source whose stamp can
// be looked up before Parse; read/size/changed/cancellation failures never do.
func (r *SummaryReader) Read(ctx context.Context, path domain.NFOSource) (app.NFOReadSource, error) {
	if err := summaryContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || domain.ValidateNFOIdentity(r.identity) != nil {
		return nil, domain.ErrNFOReaderUnavailable
	}
	source, err := ReadSource(ctx, path.RootPath, path.RelativePath, r.identity.MaxSourceBytes)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, summaryReadError(err)
	}
	stamp := source.Stamp()
	value := domain.NFOStamp{Size: stamp.Size, ModifiedUnixNano: stamp.ModifiedUnixNano, SHA256: stamp.SHA256, FingerprintVersion: stamp.FingerprintVersion}
	if domain.ValidateNFOStamp(value) != nil || value.Size > r.identity.MaxSourceBytes {
		return nil, domain.ErrNFOReaderUnavailable
	}
	return &summarySource{source: source, stamp: value}, nil
}

type summarySource struct {
	source *Source
	stamp  domain.NFOStamp
}

func (*summarySource) String() string   { return "nfo validation source (data redacted)" }
func (*summarySource) GoString() string { return "nfo validation source (data redacted)" }

func (s *summarySource) Stamp() domain.NFOStamp {
	if s == nil || s.source == nil {
		return domain.NFOStamp{}
	}
	return s.stamp
}

func (s *summarySource) Parse(ctx context.Context) (domain.NFOValidationSummary, error) {
	if err := summaryContext(ctx); err != nil {
		return domain.NFOValidationSummary{}, err
	}
	if s == nil || s.source == nil || !s.source.ready {
		return domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
	}
	document, err := s.source.Parse(ctx)
	if ctx.Err() != nil {
		return domain.NFOValidationSummary{}, ctx.Err()
	}
	if err != nil {
		return summaryParseFailure(err)
	}
	return projectSummary(ctx, document)
}

func summaryContext(ctx context.Context) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	return ctx.Err()
}

func summaryReadError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrChanged):
		return domain.ErrNFOSourceChanged
	case errors.Is(err, ErrTooLarge):
		return domain.ErrNFOSourceLimit
	case errors.Is(err, ErrRead), errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalidInput):
		return domain.ErrNFOInputUnavailable
	default:
		return domain.ErrNFOReaderUnavailable
	}
}

func summaryParseFailure(err error) (domain.NFOValidationSummary, error) {
	var code domain.NFOFailureCode
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return domain.NFOValidationSummary{}, summaryReadError(err)
	case errors.Is(err, ErrChanged), errors.Is(err, ErrTooLarge), errors.Is(err, ErrRead), errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalidInput):
		return domain.NFOValidationSummary{}, summaryReadError(err)
	case errors.Is(err, ErrInvalidXML):
		code = domain.NFOFailureInvalidXML
	case errors.Is(err, ErrUnsafeXML):
		code = domain.NFOFailureUnsafeXML
	case errors.Is(err, ErrInvalidEncoding):
		code = domain.NFOFailureInvalidEncoding
	case errors.Is(err, ErrUnsupportedEncoding):
		code = domain.NFOFailureUnsupportedEncoding
	case errors.Is(err, ErrTooComplex):
		code = domain.NFOFailureTooComplex
	default:
		return domain.NFOValidationSummary{}, summaryReadError(err)
	}
	return checkedSummary(domain.NFOValidationSummary{
		SchemaVersion: domain.NFOSummarySchemaVersion, Status: domain.NFOStatusInvalid,
		Encoding: "unknown", Root: "unknown", FailureCode: code, Issues: []domain.NFOIssue{},
	})
}

func projectSummary(ctx context.Context, document *Document) (domain.NFOValidationSummary, error) {
	if err := summaryContext(ctx); err != nil {
		return domain.NFOValidationSummary{}, err
	}
	if document == nil || len(document.Entries) < 1 || len(document.Entries) > domain.NFOEntriesMax || len(document.Issues) > domain.NFOIssueTotalMax {
		return domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
	}
	switch document.Encoding {
	case "UTF-8", "UTF-16LE", "UTF-16BE", "GBK":
	default:
		return domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
	}
	root := "unknown"
	switch document.Root {
	case "movie", "tvshow", "season", "episode", "episodedetails":
		root = document.Root
	}
	if wrapperRoot(document.Root) {
		root = "wrapper"
	}
	result := domain.NFOValidationSummary{
		SchemaVersion: domain.NFOSummarySchemaVersion, Status: domain.NFOStatusValid,
		Encoding: document.Encoding, Root: root, Entries: len(document.Entries),
		IssueCount: int64(len(document.Issues)), IssuesTruncated: len(document.Issues) > domain.NFOIssuesMax,
		Issues: make([]domain.NFOIssue, 0, min(len(document.Issues), domain.NFOIssuesMax)),
	}
	for _, issue := range document.Issues {
		if err := ctx.Err(); err != nil {
			return domain.NFOValidationSummary{}, err
		}
		value := domain.NFOIssue{Severity: issue.Severity, Code: issue.Code, Field: issue.Field, Entry: issue.Entry}
		// Unknown issue vocabulary signals a parser/projection version mismatch.
		// Do not discard it, disclose arbitrary strings, or downgrade an error.
		if !domain.ValidNFOIssue(value) || value.Entry >= result.Entries {
			return domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
		}
		if value.Severity == "error" {
			result.ErrorCount++
			result.Status = domain.NFOStatusInvalid
		} else {
			result.WarningCount++
		}
		if value.Code == "nfo_encoding_guessed" {
			result.EncodingGuessed = true
		}
		if value.Code == "nfo_unknown_root" && value.Entry == 0 {
			// A namespaced element can have a familiar local name while the
			// parser correctly treats the qualified root as unknown.
			result.Root = "unknown"
		}
		if len(result.Issues) < domain.NFOIssuesMax {
			result.Issues = append(result.Issues, value)
		}
	}
	result, err := checkedSummary(result)
	if ctx.Err() != nil {
		return domain.NFOValidationSummary{}, ctx.Err()
	}
	return result, err
}

func checkedSummary(summary domain.NFOValidationSummary) (domain.NFOValidationSummary, error) {
	if _, err := domain.MarshalNFOSummary(summary); err != nil {
		return domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
	}
	return summary, nil
}
