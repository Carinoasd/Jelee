package runtime

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
)

func tmdbPreflight(key string) func(context.Context) error {
	if key == "" {
		return nil
	}
	return func(ctx context.Context) error {
		client, err := metadata.NewTMDB(key)
		if err != nil {
			return err
		}
		defer client.Close()
		budget, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return client.ValidateCredentials(budget)
	}
}
