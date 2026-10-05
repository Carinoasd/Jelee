package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// setupTokenOutput receives the setup token when no token file is set.
// Tests replace it; the logger never sees the token because its whitelist
// would (rightly) redact it.
var setupTokenOutput io.Writer = os.Stderr

// prepareSetup checks setup at startup. When it is incomplete it issues the
// one-time setup token that the wizard API requires and hands it to the
// operator: in the token file when configured, else on standard error. An
// empty token means setup is complete and the wizard stays closed.
func prepareSetup(ctx context.Context, c config.Config, setup *app.Setup, logger *slog.Logger) (string, error) {
	status, err := setup.Status(ctx)
	if err != nil {
		return "", errors.New("cannot read setup state")
	}
	if status.Completed {
		return "", nil
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return "", errors.New("cannot generate setup token")
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	if c.SetupTokenFile != "" {
		if err = writeSetupToken(c.SetupTokenFile, token); err != nil {
			return "", errors.New("cannot write setup token file")
		}
		logger.Warn("initial setup required; setup token written to the configured token file", "component", "setup")
		return token, nil
	}
	if _, err = fmt.Fprintf(setupTokenOutput, "Jelee initial setup required. Open the web interface and enter this one-time setup token: %s\n", token); err != nil {
		return "", errors.New("cannot print setup token")
	}
	logger.Warn("initial setup required; setup token printed to standard error", "component", "setup")
	return token, nil
}

// writeSetupToken replaces the file atomically with a private one.
func writeSetupToken(path, token string) error {
	temporary := path + ".tmp"
	_ = os.Remove(temporary)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a temporary file beside the configured setup token
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(token + "\n")
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err == nil {
		err = os.Rename(temporary, path)
	}
	if err != nil {
		_ = os.Remove(temporary)
	}
	return err
}
