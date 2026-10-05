// Package legacydbtest generates synthetic upstream SQLite databases for
// legacy import tests. The schema is the subset of the EF Core model
// (tag upstream-csharp-final, Jellyfin.Database.Providers.Sqlite) that the
// import reads, with the same table, column and index names, GUIDs stored
// as upper-case text and DateTime as "yyyy-MM-dd HH:mm:ss.FFFFFFF". No real
// user data is ever involved: every row comes from the Spec.
package legacydbtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

// Migrations is a plausible history ending at the tested migration.
var Migrations = []string{
	"20200514181226_AddActivityLog", "20200613202153_AddUsers", "20241020103111_LibraryDbMigration",
	"20250609115616_DetachUserDataInsteadOfDelete", "20260815063607_RemoveOrphanedUserPermissionsAndPreferences",
}

// Upstream type names.
const (
	TypeMovie            = "MediaBrowser.Controller.Entities.Movies.Movie"
	TypeEpisode          = "MediaBrowser.Controller.Entities.TV.Episode"
	TypeSeries           = "MediaBrowser.Controller.Entities.TV.Series"
	TypeSeason           = "MediaBrowser.Controller.Entities.TV.Season"
	TypeVideo            = "MediaBrowser.Controller.Entities.Video"
	TypeAudio            = "MediaBrowser.Controller.Entities.Audio.Audio"
	TypeBook             = "MediaBrowser.Controller.Entities.Book"
	TypeLiveTvChannel    = "MediaBrowser.Controller.LiveTv.LiveTvChannel"
	TypeFolder           = "MediaBrowser.Controller.Entities.Folder"
	TypeTrailer          = "MediaBrowser.Controller.Entities.Trailer"
	TypeCollectionFolder = "MediaBrowser.Controller.Entities.CollectionFolder"
)

// User is one Users row with its permissions and preferences.
type User struct {
	ID, Name, Password string
	MaxRating          *int
	MaxSessions        int
	Bitrate            *int64
	Permissions        map[int]bool
	Preferences        map[int]string
}

// Library is a CollectionFolder; RawData replaces the generated JSON.
type Library struct {
	ID, Name, CollectionType string
	Locations                []string
	RawData                  string
}

// Item is one BaseItems row.
type Item struct {
	ID, Type, Path string
	Virtual        bool
	Extra          bool
}

// UserData is one UserData row.
type UserData struct {
	UserID, ItemID, Key string
	Position            int64
	Played              bool
	PlayCount           int
	LastPlayed          string
	Favorite            bool
}

// Spec describes a database. Items and UserData may be produced lazily
// for large fixtures.
type Spec struct {
	Migrations []string
	Users      []User
	Libraries  []Library
	Items      []Item
	UserData   []UserData
	MoreItems  func(add func(Item) error) error
	MoreData   func(add func(UserData) error) error
	// LibraryDB builds a pre-10.11 library.db instead (TypedBaseItems).
	LibraryDB bool
	// OmitColumn drops one "Table.Column" from the schema.
	OmitColumn string
}

var schema = []string{
	`CREATE TABLE "__EFMigrationsHistory" ("MigrationId" TEXT NOT NULL CONSTRAINT "PK___EFMigrationsHistory" PRIMARY KEY,"ProductVersion" TEXT NOT NULL)`,
	`CREATE TABLE "Users" ("Id" TEXT NOT NULL CONSTRAINT "PK_Users" PRIMARY KEY,"Username" TEXT NOT NULL,"Password" TEXT NULL,"MustUpdatePassword" INTEGER NOT NULL,
 "AudioLanguagePreference" TEXT NULL,"AuthenticationProviderId" TEXT NOT NULL,"PasswordResetProviderId" TEXT NOT NULL,"InvalidLoginAttemptCount" INTEGER NOT NULL,
 "LastActivityDate" TEXT NULL,"LastLoginDate" TEXT NULL,"LoginAttemptsBeforeLockout" INTEGER NULL,"MaxActiveSessions" INTEGER NOT NULL,"SubtitleMode" INTEGER NOT NULL,
 "PlayDefaultAudioTrack" INTEGER NOT NULL,"SubtitleLanguagePreference" TEXT NULL,"DisplayMissingEpisodes" INTEGER NOT NULL,"DisplayCollectionsView" INTEGER NOT NULL,
 "EnableLocalPassword" INTEGER NOT NULL,"HidePlayedInLatest" INTEGER NOT NULL,"RememberAudioSelections" INTEGER NOT NULL,"RememberSubtitleSelections" INTEGER NOT NULL,
 "EnableNextEpisodeAutoPlay" INTEGER NOT NULL,"EnableAutoLogin" INTEGER NOT NULL,"EnableUserPreferenceAccess" INTEGER NOT NULL,"MaxParentalRatingScore" INTEGER NULL,
 "MaxParentalRatingSubScore" INTEGER NULL,"RemoteClientBitrateLimit" INTEGER NULL,"InternalId" INTEGER NOT NULL,"SyncPlayAccess" INTEGER NOT NULL,"CastReceiverId" TEXT NULL,
 "NormalizedUsername" TEXT NOT NULL,"RowVersion" INTEGER NOT NULL)`,
	`CREATE UNIQUE INDEX "IX_Users_Username" ON "Users" ("Username")`,
	`CREATE UNIQUE INDEX "IX_Users_NormalizedUsername" ON "Users" ("NormalizedUsername")`,
	`CREATE TABLE "Permissions" ("Id" INTEGER NOT NULL CONSTRAINT "PK_Permissions" PRIMARY KEY AUTOINCREMENT,"UserId" TEXT NOT NULL,"Kind" INTEGER NOT NULL,"Value" INTEGER NOT NULL,"RowVersion" INTEGER NOT NULL)`,
	`CREATE UNIQUE INDEX "IX_Permissions_UserId_Kind" ON "Permissions" ("UserId","Kind")`,
	`CREATE TABLE "Preferences" ("Id" INTEGER NOT NULL CONSTRAINT "PK_Preferences" PRIMARY KEY AUTOINCREMENT,"UserId" TEXT NOT NULL,"Kind" INTEGER NOT NULL,"Value" TEXT NOT NULL,"RowVersion" INTEGER NOT NULL)`,
	`CREATE UNIQUE INDEX "IX_Preferences_UserId_Kind" ON "Preferences" ("UserId","Kind")`,
	`CREATE TABLE "BaseItems" ("Id" TEXT NOT NULL CONSTRAINT "PK_BaseItems" PRIMARY KEY,"Type" TEXT NOT NULL,"Data" TEXT NULL,"Path" TEXT NULL,"StartDate" TEXT NULL,
 "EndDate" TEXT NULL,"ChannelId" TEXT NULL,"IsMovie" INTEGER NOT NULL,"IsSeries" INTEGER NOT NULL,"Name" TEXT NULL,"IsFolder" INTEGER NOT NULL,"IsVirtualItem" INTEGER NOT NULL,
 "ExtraType" INTEGER NULL,"OwnerId" TEXT NULL,"ParentId" TEXT NULL,"TopParentId" TEXT NULL,"SeriesId" TEXT NULL,"SeasonId" TEXT NULL,"MediaType" TEXT NULL,
 "IsLocked" INTEGER NOT NULL,"IsRepeat" INTEGER NOT NULL,"IsInMixedFolder" INTEGER NOT NULL)`,
	`CREATE INDEX "IX_BaseItems_Path" ON "BaseItems" ("Path")`,
	`CREATE INDEX "IX_BaseItems_Type_TopParentId_Id" ON "BaseItems" ("Type","TopParentId","Id")`,
	`CREATE TABLE "UserData" ("ItemId" TEXT NOT NULL,"UserId" TEXT NOT NULL,"CustomDataKey" TEXT NOT NULL,"Rating" REAL NULL,"PlaybackPositionTicks" INTEGER NOT NULL,
 "PlayCount" INTEGER NOT NULL,"IsFavorite" INTEGER NOT NULL,"LastPlayedDate" TEXT NULL,"Played" INTEGER NOT NULL,"AudioStreamIndex" INTEGER NULL,"SubtitleStreamIndex" INTEGER NULL,
 "Likes" INTEGER NULL,"RetentionDate" TEXT NULL,CONSTRAINT "PK_UserData" PRIMARY KEY ("ItemId","UserId","CustomDataKey"))`,
	`CREATE INDEX "IX_UserData_UserId_ItemId_LastPlayedDate" ON "UserData" ("UserId","ItemId","LastPlayedDate")`,
}

func upper(id string) string { return strings.ToUpper(id) }

func b(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Build writes a new database at path.
func Build(ctx context.Context, path string, spec Spec) (err error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(DELETE)")
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); err == nil {
			err = closeErr
		}
	}()
	db.SetMaxOpenConns(1)
	if spec.LibraryDB {
		_, err = db.ExecContext(ctx, `CREATE TABLE TypedBaseItems (guid GUID PRIMARY KEY, type TEXT NOT NULL, data BLOB NULL, Path TEXT NULL);
 CREATE TABLE UserDatas (key TEXT NOT NULL, userId INT NOT NULL, rating FLOAT NULL, played BIT NOT NULL)`)
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, statement := range schema {
		if spec.OmitColumn != "" {
			table, column, _ := strings.Cut(spec.OmitColumn, ".")
			if strings.HasPrefix(statement, `CREATE TABLE "`+table+`"`) {
				statement = strings.Replace(statement, `"`+column+`" `, `"Renamed`+column+`" `, 1)
			}
			if strings.Contains(statement, `"`+column+`"`) && strings.Contains(statement, `ON "`+table+`"`) {
				continue
			}
		}
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: %s", err, statement)
		}
	}
	migrations := spec.Migrations
	if migrations == nil {
		migrations = Migrations
	}
	for _, m := range migrations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO "__EFMigrationsHistory" VALUES(?,'9.0.11')`, m); err != nil {
			return err
		}
	}
	// Inserts name an omitted column by its new name.
	rename := func(table, statement string) string {
		if t, column, _ := strings.Cut(spec.OmitColumn, "."); t == table {
			return strings.Replace(statement, column, "Renamed"+column, 1)
		}
		return statement
	}
	if err = insertUsers(ctx, tx, spec.Users, rename("Users", insertUser)); err != nil {
		return err
	}
	item, err := tx.PrepareContext(ctx, rename("BaseItems", `INSERT INTO BaseItems(Id,Type,Data,Path,Name,IsMovie,IsSeries,IsFolder,IsVirtualItem,ExtraType,IsLocked,IsRepeat,IsInMixedFolder) VALUES(?,?,?,?,?,0,0,?,?,?,0,0,0)`))
	if err != nil {
		return err
	}
	defer func() { _ = item.Close() }()
	// The placeholder upstream migrations insert for detached user data.
	if _, err = item.ExecContext(ctx, "00000000-0000-0000-0000-000000000001", "PLACEHOLDER", nil, nil, "This is a placeholder item", 0, 0, nil); err != nil {
		return err
	}
	for _, l := range spec.Libraries {
		data := l.RawData
		if data == "" {
			var collection any
			if l.CollectionType != "" {
				collection = l.CollectionType
			}
			raw, _ := json.Marshal(map[string]any{"PhysicalLocationsList": l.Locations, "CollectionType": collection, "Name": l.Name})
			data = string(raw)
		}
		if _, err = item.ExecContext(ctx, upper(l.ID), TypeCollectionFolder, data, "%AppDataPath%/root/default/"+l.Name, l.Name, 1, 0, nil); err != nil {
			return err
		}
	}
	addItem := func(i Item) error {
		var extra, path any
		if i.Extra {
			extra = 4
		}
		if i.Path != "" {
			path = i.Path
		}
		_, e := item.ExecContext(ctx, upper(i.ID), i.Type, nil, path, "item", b(i.Type == TypeSeries || i.Type == TypeSeason || i.Type == TypeFolder), b(i.Virtual), extra)
		return e
	}
	for _, i := range spec.Items {
		if err = addItem(i); err != nil {
			return err
		}
	}
	if spec.MoreItems != nil {
		if err = spec.MoreItems(addItem); err != nil {
			return err
		}
	}
	data, err := tx.PrepareContext(ctx, rename("UserData", `INSERT INTO UserData(ItemId,UserId,CustomDataKey,PlaybackPositionTicks,PlayCount,IsFavorite,LastPlayedDate,Played) VALUES(?,?,?,?,?,?,?,?)`))
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	addData := func(d UserData) error {
		var last any
		if d.LastPlayed != "" {
			last = d.LastPlayed
		}
		key := d.Key
		if key == "" {
			key = d.ItemID
		}
		_, e := data.ExecContext(ctx, upper(d.ItemID), upper(d.UserID), key, d.Position, d.PlayCount, b(d.Favorite), last, b(d.Played))
		return e
	}
	for _, d := range spec.UserData {
		if err = addData(d); err != nil {
			return err
		}
	}
	if spec.MoreData != nil {
		if err = spec.MoreData(addData); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const insertUser = `INSERT INTO Users(Id,Username,Password,MustUpdatePassword,AuthenticationProviderId,PasswordResetProviderId,InvalidLoginAttemptCount,
 MaxActiveSessions,SubtitleMode,PlayDefaultAudioTrack,DisplayMissingEpisodes,DisplayCollectionsView,EnableLocalPassword,HidePlayedInLatest,RememberAudioSelections,
 RememberSubtitleSelections,EnableNextEpisodeAutoPlay,EnableAutoLogin,EnableUserPreferenceAccess,MaxParentalRatingScore,RemoteClientBitrateLimit,InternalId,SyncPlayAccess,
 NormalizedUsername,RowVersion) VALUES(?,?,?,0,'DefaultAuthenticationProvider','DefaultPasswordResetProvider',0,?,0,1,0,0,0,1,1,1,1,0,1,?,?,?,0,?,0)`

func insertUsers(ctx context.Context, tx *sql.Tx, users []User, statement string) error {
	for n, u := range users {
		var password, rating, bitrate any
		if u.Password != "" {
			password = u.Password
		}
		if u.MaxRating != nil {
			rating = *u.MaxRating
		}
		if u.Bitrate != nil {
			bitrate = *u.Bitrate
		}
		if _, err := tx.ExecContext(ctx, statement,
			upper(u.ID), u.Name, password, u.MaxSessions, rating, bitrate, n+1, strings.ToUpper(u.Name)); err != nil {
			return err
		}
		for kind, value := range u.Permissions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO Permissions(UserId,Kind,Value,RowVersion) VALUES(?,?,?,0)`, upper(u.ID), kind, b(value)); err != nil {
				return err
			}
		}
		for kind, value := range u.Preferences {
			if _, err := tx.ExecContext(ctx, `INSERT INTO Preferences(UserId,Kind,Value,RowVersion) VALUES(?,?,?,0)`, upper(u.ID), kind, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// AddOrphanRows inserts permission and preference rows of a user that no
// longer exists, as older upstream versions left behind.
func AddOrphanRows(ctx context.Context, path, userID string) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, `INSERT INTO Permissions(UserId,Kind,Value,RowVersion) VALUES(?,0,1,0); INSERT INTO Preferences(UserId,Kind,Value,RowVersion) VALUES(?,5,'',0)`, upper(userID), upper(userID))
	return err
}
