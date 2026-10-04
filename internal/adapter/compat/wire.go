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
