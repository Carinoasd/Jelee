package domain

import (
	"encoding/json"
	"time"
)

// UserDataExportFormat names the personal data export (G07.7): newline
// delimited JSON, one UserDataRecord per line. The first record has type
// "export" and carries the UserDataExportHeader; a complete export ends with
// a record of type "end" that carries the number of data records. A stream
// without the end record was cut short.
const (
	UserDataExportFormat  = "jelee.user-data"
	UserDataExportVersion = 1
)

// UserDataExportTimeout bounds one export stream, including its snapshot.
const UserDataExportTimeout = 5 * time.Minute

// UserDataPurgeTimeout bounds the statements of one permanent deletion.
const UserDataPurgeTimeout = 30 * time.Second

// UserDataExportHeader describes an export. GeneratedAt is the database time
// of the snapshot every record was read from.
type UserDataExportHeader struct {
	Format      string    `json:"format"`
	Version     int       `json:"version"`
	UserID      string    `json:"userId"`
	UserName    string    `json:"userName"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// UserDataRecord is one line of an export. Type names the section (account,
// preferences, itemData, playbackSession, session, ...); Data is the row as a
// JSON object. Secrets (password and application password digests,
// authenticator secrets, recovery code digests, session and share tokens)
// are never part of any record.
type UserDataRecord struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// UserPurgeProof is the re-authentication of a user deleting their own
// account: the credential snapshot whose password the caller verified and,
// when the account has a second factor, exactly one of an authenticator code
// verifier or a recovery code digest.
type UserPurgeProof struct {
	Expected Credentials
	Verify   TOTPVerifier
	Recovery []byte
}
