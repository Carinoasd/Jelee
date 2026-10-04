package compat

// Legacy client authentication wire names. These are protocol strings sent by
// existing third-party clients and must be matched verbatim. The whole compat
// package is a protocol boundary exempt from the brand scan (see
// tools/brand-scan/allowlist.txt); the wire names are still kept in this one
// file so they are easy to audit, and new code should reference them here.
//
// Behavioural reference (read, not ported line by line):
// Jellyfin.Server.Implementations/Security/AuthorizationContext.cs
// (GetAuthorizationDictionary, GetAuthorization, GetParts and
// GetAuthorizationInfoFromDictionary) and its GetParts test data in
// tests/Jellyfin.Api.Tests/Auth/DefaultAuthorizationPolicy/DefaultAuthorizationHandlerTests.cs.
const (
	// schemePrimary is the parameterised Authorization scheme accepted
	// unconditionally upstream.
	schemePrimary = "MediaBrowser"
	// schemeLegacy is accepted upstream only with legacy authorization enabled.
	schemeLegacy = "Emby"
	// headerLegacyAuthorization is consulted only when Authorization is empty.
	headerLegacyAuthorization = "X-Emby-Authorization"
	// headerLegacyToken and headerLegacyTokenAlt carry a bare access token.
	headerLegacyToken    = "X-Emby-Token"
	headerLegacyTokenAlt = "X-MediaBrowser-Token"
)

// System module behavioural reference (field names and casing): upstream
// Jellyfin.Api/Controllers/SystemController.cs (GetSystemInfo,
// GetPublicSystemInfo, PingSystem), Emby.Server.Implementations/SystemManager.cs
// and MediaBrowser.Model/System/PublicSystemInfo.cs and SystemInfo.cs. The
// serializer defaults (PascalCase, null members omitted) come from
// src/Jellyfin.Extensions/Json/JsonDefaults.cs and the error behaviour from
// Jellyfin.Api/Middleware/ExceptionMiddleware.cs. None of the system DTO field
// names carries an upstream brand, so no wire constant is needed for them.

// User module behavioural reference: upstream
// Jellyfin.Api/Controllers/UserController.cs (AuthenticateUserByName,
// GetCurrentUser, GetUserById, GetPublicUsers),
// Jellyfin.Api/Controllers/SessionController.cs (ReportSessionEnded),
// MediaBrowser.Model/Dto/UserDto.cs, MediaBrowser.Model/Users/UserPolicy.cs,
// MediaBrowser.Model/Configuration/UserConfiguration.cs,
// MediaBrowser.Model/Dto/SessionInfoDto.cs and
// MediaBrowser.Controller/Authentication/AuthenticationResult.cs; refusal
// statuses from Jellyfin.Server.Implementations/Users/UserManager.cs
// (AuthenticateUser) and the exception middleware. The DTO member names and
// enum values (SubtitleMode "Default", SyncPlayAccess "None") carry no
// upstream brand. The policy provider identifiers are Jelee names (see
// users.go), not the upstream provider type names.

// Library module behavioural reference: upstream
// Jellyfin.Api/Controllers/UserViewsController.cs (GetUserViews and its
// legacy route), Jellyfin.Api/Controllers/ItemsController.cs (GetItems and
// GetItemsByUserIdLegacy), Jellyfin.Api/Controllers/UserLibraryController.cs
// (GetItem and its legacy route), Jellyfin.Api/Helpers/RequestHelpers.cs
// (GetOrderBy, GetUserId), Emby.Server.Implementations/Dto/DtoService.cs
// (which members depend on Fields), MediaBrowser.Model/Dto/BaseItemDto.cs,
// MediaBrowser.Model/Dto/UserItemDataDto.cs,
// MediaBrowser.Model/Dto/MediaSourceInfo.cs,
// MediaBrowser.Model/Entities/MediaStream.cs and
// MediaBrowser.Model/Querying/QueryResult.cs. The values below are upstream
// enum names (BaseItemKind, CollectionType, ItemFields, ItemSortBy,
// SortOrder, LocationType, MediaType, MediaProtocol, MediaSourceType,
// MediaStreamType); none carries an upstream brand.
const (
	itemTypeMovie            = "Movie"
	itemTypeSeries           = "Series"
	itemTypeSeason           = "Season"
	itemTypeEpisode          = "Episode"
	itemTypeVideo            = "Video"
	itemTypeCollectionFolder = "CollectionFolder"

	collectionTypeMovies     = "movies"
	collectionTypeTvShows    = "tvshows"
	collectionTypeHomeVideos = "homevideos"

	fieldOverview     = "overview"
	fieldSortName     = "sortname"
	fieldParentID     = "parentid"
	fieldMediaSources = "mediasources"
	// fieldPrimaryImageAspectRatio asks for PrimaryImageAspectRatio.
	fieldPrimaryImageAspectRatio = "primaryimageaspectratio"

	sortBySortName       = "sortname"
	sortByName           = "name"
	sortByPremiereDate   = "premieredate"
	sortByProductionYear = "productionyear"

	sortOrderAscending  = "ascending"
	sortOrderDescending = "descending"

	locationFileSystem  = "FileSystem"
	mediaTypeVideo      = "Video"
	mediaTypeUnknown    = "Unknown"
	mediaProtocolFile   = "File"
	mediaSourceDefault  = "Default"
	mediaStreamVideo    = "Video"
	mediaStreamAudio    = "Audio"
	mediaStreamSubtitle = "Subtitle"
)

// itemTypeByKind maps catalog kinds to upstream item types. A home video is
// the generic upstream video type.
var itemTypeByKind = map[string]string{
	"Movie":     itemTypeMovie,
	"Series":    itemTypeSeries,
	"Season":    itemTypeSeason,
	"Episode":   itemTypeEpisode,
	"HomeVideo": itemTypeVideo,
}

// Playback module behavioural reference: upstream
// Jellyfin.Api/Controllers/MediaInfoController.cs (GetPlaybackInfo,
// GetPostedPlaybackInfo), Jellyfin.Api/Helpers/MediaInfoHelper.cs
// (GetPlaybackInfo, SetDeviceSpecificData),
// Jellyfin.Api/Controllers/VideosController.cs (GetVideoStream and its
// container route), Jellyfin.Api/Controllers/AudioController.cs,
// Jellyfin.Api/Controllers/SubtitleController.cs (GetSubtitle and its start
// position route), MediaBrowser.Model/MediaInfo/PlaybackInfoResponse.cs,
// MediaBrowser.Model/Dlna/PlaybackErrorCode.cs, DlnaProfileType.cs,
// SubtitleDeliveryMethod.cs, DirectPlayProfile.cs and StreamInfo.cs (the
// subtitle URL form). The values below are upstream enum names.
const (
	playbackErrorNoCompatibleStream = "NoCompatibleStream"
	subtitleDeliveryEmbed           = "Embed"
	subtitleDeliveryExternal        = "External"

	dlnaProfileTypeVideo       profileType = "Video"
	dlnaProfileTypeVideoNumber             = 1
)

// Image module behavioural reference: upstream
// Jellyfin.Api/Controllers/ImageController.cs (GetItemImage,
// GetItemImageByIndex, GetImageResult), Emby.Server.Implementations/Dto/
// DtoService.cs (image tags, backdrop tags, GetPrimaryImageAspectRatio),
// MediaBrowser.Controller/Dto/DtoOptions.cs (GetImageLimit),
// MediaBrowser.Model/Entities/ImageType.cs and
// MediaBrowser.Model/Drawing/ImageFormat.cs. The values below are upstream
// ImageType names, which are also the ImageTags keys.
const (
	imageTypePrimary    = "Primary"
	imageTypeArt        = "Art"
	imageTypeBackdrop   = "Backdrop"
	imageTypeBanner     = "Banner"
	imageTypeLogo       = "Logo"
	imageTypeThumb      = "Thumb"
	imageTypeDisc       = "Disc"
	imageTypeBox        = "Box"
	imageTypeScreenshot = "Screenshot"
	imageTypeMenu       = "Menu"
	imageTypeChapter    = "Chapter"
	imageTypeBoxRear    = "BoxRear"
	imageTypeProfile    = "Profile"
)
