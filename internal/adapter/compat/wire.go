package compat

// Legacy client authentication wire names. These are protocol strings sent by
// existing third-party clients and must be matched verbatim; they are kept in
// this single file so the brand scan exception stays narrow (see
// tools/brand-scan/allowlist.txt). No other identifier in this package may
// carry these names.
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
