package runtime

import (
	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
)

var _ app.MetadataImageProvider = (*metadata.TMDB)(nil)

func bindMetadata(service *app.Metadata, repository interface {
	app.MetadataPreferencesRepository
	app.ItemMetadataRepository
}) (*app.Metadata, error) {
	if service == nil {
		return app.NewLocalMetadata(repository)
	}
	bound, err := service.WithLibraryPreferences(repository)
	if err != nil {
		return nil, err
	}
	return bound.WithItemMetadata(repository)
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
