package runtime

import (
	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
)

// The TMDB adapter resolves NFO IMDb and TVDB identifiers for metadata
// refresh jobs (G14.4).
var _ app.MetadataRefreshFinder = (*metadata.TMDB)(nil)
