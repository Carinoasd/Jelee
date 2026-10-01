package runtime

import (
	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
)

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
