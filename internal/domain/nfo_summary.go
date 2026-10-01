package domain

import "encoding/json"

// ValidNFOIssue rejects arbitrary text even when it resembles a known code.
// Combinations mirror the fixed parser's emitted code/severity/field contract.
// Entry ownership is additionally checked against the summary's Entries count.
func ValidNFOIssue(v NFOIssue) bool {
	if v.Entry < -1 || v.Entry >= NFOEntriesMax {
		return false
	}
	if v.Code == "nfo_encoding_guessed" {
		return v.Severity == "warning" && v.Field == "encoding" && v.Entry == -1
	}
	if v.Entry < 0 {
		return false
	}
	if v.Severity == "warning" {
		switch v.Code {
		case "nfo_unknown_root", "nfo_wrapper_fields_ignored", "nfo_kind_unknown":
			return v.Field == "root"
		case "nfo_title_missing":
			return v.Field == "title"
		case "nfo_season_missing":
			return v.Field == "season"
		case "nfo_episode_missing":
			return v.Field == "episode"
		case "nfo_conflicting_id":
			return v.Field == "uniqueid"
		}
		return false
	}
	if v.Severity != "error" {
		return false
	}
	switch v.Code {
	case "nfo_invalid_integer":
		switch v.Field {
		case "year", "season", "seasonnumber", "episode", "displayseason", "displayepisode", "runtime", "actor", "thumb", "poster", "banner", "clearart", "clearlogo", "landscape", "ratings":
			return true
		}
	case "nfo_invalid_number":
		return v.Field == "rating" || v.Field == "communityrating" || v.Field == "userrating" || v.Field == "ratings"
	case "nfo_invalid_boolean":
		return v.Field == "uniqueid" || v.Field == "ratings" || v.Field == "lockdata"
	case "nfo_person_name_missing":
		return v.Field == "actor"
	case "nfo_id_incomplete":
		return v.Field == "uniqueid" || v.Field == "imdbid" || v.Field == "tmdbid" || v.Field == "tvdbid" || v.Field == "id"
	case "nfo_invalid_rating_scale", "nfo_rating_value_missing":
		return v.Field == "ratings"
	case "nfo_invalid_date":
		return v.Field == "premiered" || v.Field == "aired" || v.Field == "dateadded"
	case "nfo_unsafe_reference":
		return v.Field == "art" || v.Field == "art.preview" || v.Field == "actor.thumb"
	}
	return false
}

func ValidNFOEncoding(value string) bool {
	switch value {
	case "UTF-8", "UTF-16LE", "UTF-16BE", "GBK", "unknown":
		return true
	}
	return false
}

func ValidNFORoot(value string) bool {
	switch value {
	case "movie", "tvshow", "season", "episode", "episodedetails", "wrapper", "unknown":
		return true
	}
	return false
}

func ValidateNFOSummary(v NFOValidationSummary) error {
	if len(v.Issues) > NFOIssuesMax {
		return ErrNFOSummaryLimit
	}
	if v.SchemaVersion != NFOSummarySchemaVersion || !ValidNFOEncoding(v.Encoding) || !ValidNFORoot(v.Root) ||
		(v.Status != NFOStatusValid && v.Status != NFOStatusInvalid) || v.Entries < 0 || v.Entries > NFOEntriesMax ||
		v.WarningCount < 0 || v.WarningCount > NFOIssueTotalMax || v.ErrorCount < 0 || v.ErrorCount > NFOIssueTotalMax ||
		v.IssueCount < 0 || v.IssueCount > NFOIssueTotalMax || v.IssueCount != v.WarningCount+v.ErrorCount ||
		int64(len(v.Issues)) != min(v.IssueCount, NFOIssuesMax) || v.IssuesTruncated != (v.IssueCount > NFOIssuesMax) {
		return ErrInvalid
	}
	if v.FailureCode != "" {
		if !ValidNFOParseFailureCode(v.FailureCode) || v.Status != NFOStatusInvalid || v.Encoding != "unknown" || v.Root != "unknown" ||
			v.Entries != 0 || v.EncodingGuessed || v.IssueCount != 0 {
			return ErrInvalid
		}
		return nil
	}
	if v.Entries < 1 || v.Encoding == "unknown" || (v.Status == NFOStatusInvalid) != (v.ErrorCount > 0) {
		return ErrInvalid
	}
	var warnings, errors int64
	guessed := false
	for _, issue := range v.Issues {
		if !ValidNFOIssue(issue) || issue.Entry >= v.Entries {
			return ErrInvalid
		}
		if issue.Severity == "warning" {
			warnings++
		} else {
			errors++
		}
		if issue.Code == "nfo_encoding_guessed" {
			if guessed {
				return ErrInvalid
			}
			guessed = true
		}
	}
	if warnings > v.WarningCount || errors > v.ErrorCount || !v.IssuesTruncated && (warnings != v.WarningCount || errors != v.ErrorCount) ||
		guessed != v.EncodingGuessed || v.EncodingGuessed && (v.Encoding == "UTF-8" || v.WarningCount == 0) {
		return ErrInvalid
	}
	return nil
}

// MarshalNFOSummary is the persistence boundary; callers must not serialize an
// arbitrary adapter Document. The fixed struct order and empty array encoding
// are deterministic. PostgreSQL must separately bound its jsonb::text bytes.
func MarshalNFOSummary(v NFOValidationSummary) ([]byte, error) {
	if err := ValidateNFOSummary(v); err != nil {
		return nil, err
	}
	if v.Issues == nil {
		v.Issues = []NFOIssue{}
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(encoded) > NFOSummaryMaxBytes {
		return nil, ErrNFOSummaryLimit
	}
	return encoded, nil
}
