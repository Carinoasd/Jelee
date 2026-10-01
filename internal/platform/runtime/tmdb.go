package runtime

import (
	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.MetadataImageProvider = (*metadata.TMDB)(nil)

func bindMetadata(service *app.Metadata, repository interface {
	app.MetadataPreferencesRepository
	app.ItemMetadataRepository
}) (*app.Metadata, error) {
	var bound *app.Metadata
	var err error
	if service == nil {
		bound, err = app.NewLocalMetadata(repository)
	} else {
		bound, err = service.WithLibraryPreferences(repository)
		if err == nil {
			bound, err = bound.WithItemMetadata(repository)
		}
	}
	if err != nil {
		return nil, err
	}
	reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		return nil, err
	}
	return bound.WithNFOItemFields(reader)
}

func prepareMetadata(key string, l *lifetime) (*app.Metadata, error) {
	if key == "" {
		return nil, nil
	}
	client, err := metadata.NewTMDB(key)
	if err != nil {
		return nil, err
	}
	l.prepareTMDB = client.ValidateCredentials
	l.closeTMDB = client.Close
	return app.NewMetadata(client)
}
