package domain

import "time"

// NFOFieldLockOrigin records positive lock intent independently of text values.
// It contains historical source identity and the complete original-byte stamp.
type NFOFieldLockOrigin struct {
	SourceID       string                  `json:"sourceId"`
	RootID         string                  `json:"rootId"`
	Generation     int64                   `json:"generation"`
	Stamp          NFOItemObservationStamp `json:"stamp"`
	IdentityDigest string                  `json:"identityDigest"`
	Projection     string                  `json:"projection"`
	ReadAt         time.Time               `json:"readAt"`
	Locked         bool                    `json:"locked"`
}

func ValidNFOFieldLockOrigin(v NFOFieldLockOrigin) bool {
	return v.Locked && ValidID(v.SourceID) && ValidID(v.RootID) && v.Generation >= 1 && ValidateNFOStamp(NFOStamp{Size: v.Stamp.Size, ModifiedUnixNano: v.Stamp.ModifiedUnixNano, SHA256: v.Stamp.SHA256, FingerprintVersion: v.Stamp.FingerprintVersion}) == nil && v.Stamp.Size <= 32<<20 && probeHex(v.IdentityDigest, 64) && (v.Projection == NFOItemFieldsVersion || v.Projection == NFOItemLockFieldsVersion || v.Projection == NFOItemSortFieldsVersion || v.Projection == NFOItemTextFieldsVersion || v.Projection == NFOItemYearFieldsVersion || v.Projection == NFOItemNumericFieldsVersion || v.Projection == NFOItemListFieldsVersion || v.Projection == NFOItemActorFieldsVersion || v.Projection == NFOItemIdentifierFieldsVersion || v.Projection == NFOItemRatingFieldsVersion || v.Projection == NFOItemCollectionFieldsVersion || v.Projection == NFOItemMovieFieldsVersion) && !v.ReadAt.IsZero() && v.ReadAt.Year() >= 1 && v.ReadAt.Year() <= 9999
}
