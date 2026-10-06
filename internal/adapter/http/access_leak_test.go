package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// G48.3 / G48.10: hidden content must not leak through any registered route.
// Every route that chi reports must be classified below. A new route that is
// neither covered nor explicitly exempted with a reason fails
// TestAccessLeakRouteTableIsComplete, even without a database.

type leakMode int

const (
	// leakByID addresses media by ID for non-administrators. Hidden IDs must
	// be answered exactly like missing IDs with the configured hidden status.
	leakByID leakMode = iota + 1
	// leakList lists content for non-administrators; hidden media must be absent.
	leakList
	// leakAdmin is administrator-only. A viewer is refused before any lookup,
	// so hidden and missing IDs must produce the same 403.
	leakAdmin
	// leakNoMedia carries no media identifiers. It is still requested by the
	// viewer and its whole response is scanned for hidden markers.
	leakNoMedia
	// leakExempt is not requested; reason must explain why.
	leakExempt
)

type leakRoute struct {
	mode leakMode
	// params maps every path parameter to a fixture kind (see leakIDs.value).
	params map[string]string
	// control asks for an administrator request proving the fixture exposes
	// the hidden marker when authorization allows it.
	control bool
	reason  string
}

var (
	itemParam   = map[string]string{"id": "item"}
	sourceParam = map[string]string{"id": "source"}
	libParam    = map[string]string{"id": "library"}
	jobParam    = map[string]string{"id": "job"}
	selfParam   = map[string]string{"id": "self"}
	tmdbParam   = map[string]string{"id": "tmdb"}
	// webhookParam names a webhook endpoint, never media.
	webhookParam = map[string]string{"id": "opaque"}
	// shareParam names a share link or a network rule, never media.
	shareParam = map[string]string{"id": "opaque"}
	noParams   = map[string]string{}
)

func leakRouteTable() map[string]leakRoute {
	admin := func(p map[string]string) leakRoute { return leakRoute{mode: leakAdmin, params: p} }
	noMedia := func(p map[string]string, reason string) leakRoute {
		return leakRoute{mode: leakNoMedia, params: p, reason: reason}
	}
	exempt := func(reason string) leakRoute { return leakRoute{mode: leakExempt, reason: reason} }
	return map[string]leakRoute{
		// Public service routes.
		"GET /healthz":             noMedia(noParams, "public liveness status"),
		"GET /readyz":              noMedia(noParams, "public readiness status"),
		"GET /api/v1/system":       noMedia(noParams, "public capability flags"),
		"GET /api-docs":            noMedia(noParams, "API reference rendered from the generated specification"),
		"GET /api/v1/openapi.json": noMedia(noParams, "generated specification"),
		// G18 setup wizard. Once setup is complete every wizard path answers
		// 410 before any handler runs; the wizard never names media.
		"GET /api/v1/setup/status":        noMedia(noParams, "setup wizard availability; 410 after setup"),
		"GET /api/v1/setup":               noMedia(noParams, "setup wizard state; 410 after setup"),
		"POST /api/v1/setup/steps/{step}": exempt("setup wizard step; token protected, 410 after setup, carries no media identifiers"),
		"POST /api/v1/setup/back":         exempt("setup wizard navigation; token protected, 410 after setup"),
		"POST /api/v1/setup/complete":     exempt("setup wizard completion; token protected, 410 after setup"),

		// G45 developer mode routes, registered on a developer capable
		// instance only. None takes or returns media identifiers.
		"POST /api/v1/dev/token":           exempt("loopback-only one-time developer mode token; 404 to any other peer, carries no media identifiers"),
		"GET /api/v1/dev":                  admin(noParams),
		"PUT /api/v1/dev/toggles/{toggle}": exempt("administrator-only developer mode toggle switch; names a catalogue toggle, never media"),
		"POST /api/v1/dev/disable":         admin(noParams),
		"GET /debug/pprof/*":               exempt("developer mode runtime profiles; 404 unless a session and debug_pprof are on, then loopback or administrators only"),
		"POST /debug/pprof/symbol":         exempt("developer mode symbol lookup; 404 unless a session and debug_pprof are on, then loopback or administrators only"),

		// Third-party client compatibility layer (system module).
		"GET /compat/System/Info/Public": noMedia(noParams, "public compatibility server identity"),
		"GET /compat/System/Info":        noMedia(noParams, "compatibility server information; no media identifiers"),
		"GET /compat/System/Ping":        noMedia(noParams, "returns only the product name"),
		"POST /compat/System/Ping":       exempt("returns only the product name; carries no media identifiers"),
		// Third-party client compatibility layer (user module).
		"POST /compat/Users/AuthenticateByName": exempt("native credential exchange through the shared login; takes no media identifiers and returns only a session grant"),
		"GET /compat/Users/Public":              noMedia(noParams, "always an empty list; publishes no accounts"),
		"GET /compat/Users/Me":                  noMedia(noParams, "caller's own account"),
		"GET /compat/Users/{id}":                noMedia(selfParam, "caller's own account"),
		"POST /compat/Sessions/Logout":          exempt("revokes the caller's own session; carries no media identifiers"),
		// Third-party client compatibility layer (library module). The
		// {id} member names the user a request reads as; the viewer uses its
		// own. Administrator controls run only where the administrator reads
		// as itself.
		"GET /compat/UserViews":                 {mode: leakList, params: noParams, control: true},
		"GET /compat/Users/{id}/Views":          {mode: leakList, params: selfParam},
		"GET /compat/Items":                     {mode: leakList, params: noParams, control: true},
		"GET /compat/Users/{id}/Items":          {mode: leakList, params: selfParam},
		"GET /compat/Items/{itemId}":            {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"GET /compat/Users/{id}/Items/{itemId}": {mode: leakByID, params: map[string]string{"id": "self", "itemId": "item"}},
		// Third-party client compatibility layer (playback module). Streams
		// and subtitles resolve the item with the caller's session before
		// the shared delivery handler runs.
		"GET /compat/Items/{itemId}/PlaybackInfo":                                                             {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"POST /compat/Items/{itemId}/PlaybackInfo":                                                            {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"GET /compat/Videos/{itemId}/stream":                                                                  {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"GET /compat/Videos/{itemId}/stream.{container}":                                                      {mode: leakByID, params: map[string]string{"itemId": "item", "container": "compat-container"}, control: true},
		"GET /compat/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/Stream.{format}":                       {mode: leakByID, params: map[string]string{"itemId": "item", "mediaSourceId": "source", "index": "compat-subtitle-index", "format": "compat-subtitle-format"}, control: true},
		"GET /compat/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/{startPositionTicks}/Stream.{format}":  {mode: leakByID, params: map[string]string{"itemId": "item", "mediaSourceId": "source", "index": "compat-subtitle-index", "format": "compat-subtitle-format", "startPositionTicks": "compat-zero"}, control: true},
		"GET /compat/Audio/{itemId}/stream":                                                                   noMedia(map[string]string{"itemId": "item"}, "the catalog has no audio items; every identifier is answered as missing"),
		"GET /compat/Audio/{itemId}/stream.{container}":                                                       noMedia(map[string]string{"itemId": "item", "container": "compat-container"}, "the catalog has no audio items; every identifier is answered as missing"),
		"HEAD /compat/Videos/{itemId}/stream":                                                                 {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"HEAD /compat/Videos/{itemId}/stream.{container}":                                                     {mode: leakByID, params: map[string]string{"itemId": "item", "container": "compat-container"}, control: true},
		"HEAD /compat/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/Stream.{format}":                      {mode: leakByID, params: map[string]string{"itemId": "item", "mediaSourceId": "source", "index": "compat-subtitle-index", "format": "compat-subtitle-format"}, control: true},
		"HEAD /compat/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/{startPositionTicks}/Stream.{format}": {mode: leakByID, params: map[string]string{"itemId": "item", "mediaSourceId": "source", "index": "compat-subtitle-index", "format": "compat-subtitle-format", "startPositionTicks": "compat-zero"}, control: true},
		"HEAD /compat/Audio/{itemId}/stream":                                                                  noMedia(map[string]string{"itemId": "item"}, "the catalog has no audio items; every identifier is answered as missing"),
		"HEAD /compat/Audio/{itemId}/stream.{container}":                                                      noMedia(map[string]string{"itemId": "item", "container": "compat-container"}, "the catalog has no audio items; every identifier is answered as missing"),
		// Third-party client compatibility layer (playstate module). Reports
		// name the item in the body and are answered 204 for visible, hidden
		// and missing items alike; TestProgressHTTPPostgres checks that a
		// hidden item records nothing. User data and the resume list read
		// with the library grants of the user a request reads as.
		"POST /compat/Sessions/Playing":                  exempt("playback report; the item is in the body and every outcome is an empty 204"),
		"POST /compat/Sessions/Playing/Progress":         exempt("playback report; the item is in the body and every outcome is an empty 204"),
		"POST /compat/Sessions/Playing/Stopped":          exempt("playback report; the item is in the body and every outcome is an empty 204"),
		"POST /compat/Sessions/Playing/Ping":             exempt("keeps the caller's own play session alive; carries no media identifiers and returns an empty 204"),
		"POST /compat/UserPlayedItems/{itemId}":          {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"DELETE /compat/UserPlayedItems/{itemId}":        {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"POST /compat/Users/{id}/PlayedItems/{itemId}":   {mode: leakByID, params: map[string]string{"id": "self", "itemId": "item"}},
		"DELETE /compat/Users/{id}/PlayedItems/{itemId}": {mode: leakByID, params: map[string]string{"id": "self", "itemId": "item"}},
		"GET /compat/UserItems/{itemId}/UserData":        {mode: leakByID, params: map[string]string{"itemId": "item"}, control: true},
		"GET /compat/Users/{id}/Items/{itemId}/UserData": {mode: leakByID, params: map[string]string{"id": "self", "itemId": "item"}},
		"GET /compat/UserItems/Resume":                   {mode: leakList, params: noParams, control: true},
		"GET /compat/Users/{id}/Items/Resume":            {mode: leakList, params: selfParam},
		// Third-party client compatibility layer (image module). Images go
		// through the /images pipeline with the caller's own grant.
		"GET /compat/Items/{itemId}/Images/{imageType}":               {mode: leakByID, params: map[string]string{"itemId": "item", "imageType": "image-type"}, control: true},
		"HEAD /compat/Items/{itemId}/Images/{imageType}":              {mode: leakByID, params: map[string]string{"itemId": "item", "imageType": "image-type"}, control: true},
		"GET /compat/Items/{itemId}/Images/{imageType}/{imageIndex}":  {mode: leakByID, params: map[string]string{"itemId": "item", "imageType": "image-type", "imageIndex": "compat-zero"}, control: true},
		"HEAD /compat/Items/{itemId}/Images/{imageType}/{imageIndex}": {mode: leakByID, params: map[string]string{"itemId": "item", "imageType": "image-type", "imageIndex": "compat-zero"}, control: true},

		// Catalog and delivery: the direct media surfaces.
		"GET /api/v1/items":                             {mode: leakList, params: noParams, control: true},
		"GET /api/v1/items/{id}":                        {mode: leakByID, params: itemParam, control: true},
		"GET /api/v1/items/{id}/details":                {mode: leakByID, params: itemParam, control: true},
		"GET /api/v1/items/{id}/sources":                {mode: leakByID, params: itemParam, control: true},
		"GET /api/v1/sources/{id}/stream":               {mode: leakByID, params: sourceParam, control: true},
		"HEAD /api/v1/sources/{id}/stream":              {mode: leakByID, params: sourceParam, control: true},
		"GET /api/v1/sources/{id}/subtitles/{trackId}":  {mode: leakByID, params: map[string]string{"id": "source", "trackId": "subtitle-track"}, control: true},
		"HEAD /api/v1/sources/{id}/subtitles/{trackId}": {mode: leakByID, params: map[string]string{"id": "source", "trackId": "subtitle-track"}, control: true},
		"GET /api/v1/sources/{id}/audio/{trackId}":      {mode: leakByID, params: map[string]string{"id": "source", "trackId": "audio-track"}, control: true},
		"HEAD /api/v1/sources/{id}/audio/{trackId}":     {mode: leakByID, params: map[string]string{"id": "source", "trackId": "audio-track"}, control: true},
		// Embedded items copied out of Matroska sources (G15.5, G15.7). The
		// fixture resolver authorizes through the same store lookup.
		"GET /api/v1/sources/{id}/embedded-subtitles/{index}":  {mode: leakByID, params: map[string]string{"id": "source", "index": "compat-zero"}, control: true},
		"HEAD /api/v1/sources/{id}/embedded-subtitles/{index}": {mode: leakByID, params: map[string]string{"id": "source", "index": "compat-zero"}, control: true},
		"GET /api/v1/sources/{id}/attachments/{attachmentId}":  {mode: leakByID, params: map[string]string{"id": "source", "attachmentId": "attachment-id"}, control: true},
		"HEAD /api/v1/sources/{id}/attachments/{attachmentId}": {mode: leakByID, params: map[string]string{"id": "source", "attachmentId": "attachment-id"}, control: true},
		// SRT derived by subtitle OCR (G15.6), through the same resolver.
		"GET /api/v1/sources/{id}/ocr-subtitles/{index}":                   {mode: leakByID, params: map[string]string{"id": "source", "index": "compat-zero"}, control: true},
		"HEAD /api/v1/sources/{id}/ocr-subtitles/{index}":                  {mode: leakByID, params: map[string]string{"id": "source", "index": "compat-zero"}, control: true},
		"GET /compat/Videos/{itemId}/{mediaSourceId}/Attachments/{index}":  {mode: leakByID, params: map[string]string{"itemId": "item", "mediaSourceId": "source", "index": "compat-zero"}, control: true},
		"HEAD /compat/Videos/{itemId}/{mediaSourceId}/Attachments/{index}": {mode: leakByID, params: map[string]string{"itemId": "item", "mediaSourceId": "source", "index": "compat-zero"}, control: true},
		"GET /api/v1/items/{id}/playback":                                  {mode: leakByID, params: itemParam, control: true},
		"POST /api/v1/items/{id}/playback/check":                           {mode: leakByID, params: itemParam, control: true},
		// Playback progress (G23, G48.3). Reports name the item in the body;
		// TestProgressHTTPPostgres checks hidden items are refused like
		// missing ones.
		"POST /api/v1/playback/start":              exempt("native playback report; the item is in the body and hidden items are answered like missing ones (TestProgressHTTPPostgres)"),
		"POST /api/v1/playback/progress":           exempt("native playback report; the item is in the body and hidden items are answered like missing ones (TestProgressHTTPPostgres)"),
		"POST /api/v1/playback/stop":               exempt("native playback report; the item is in the body and hidden items are answered like missing ones (TestProgressHTTPPostgres)"),
		"GET /api/v1/playback/sessions":            admin(noParams),
		"GET /api/v1/items/{id}/user-data":         {mode: leakByID, params: itemParam, control: true},
		"PUT /api/v1/items/{id}/played":            {mode: leakByID, params: itemParam, control: true},
		"DELETE /api/v1/items/{id}/played":         {mode: leakByID, params: itemParam, control: true},
		"GET /api/v1/users/me/resume":              {mode: leakList, params: noParams, control: true},
		"DELETE /api/v1/users/me/playback-history": exempt("deletes the caller's own playback history; carries no media identifiers"),
		// Version decisions (G20.3, G20.5) are administrator-only; track
		// preferences (G16.5) address the item by ID for every user, and a
		// "{}" body keeps the visible control a valid replacement.
		"GET /api/v1/items/{id}/versions":                             admin(itemParam),
		"POST /api/v1/items/{id}/versions/split":                      admin(itemParam),
		"POST /api/v1/items/{id}/versions/merge":                      admin(itemParam),
		"PUT /api/v1/items/{id}/versions/primary":                     admin(itemParam),
		"DELETE /api/v1/items/{id}/versions/exclusions/{exclusionId}": admin(map[string]string{"id": "item", "exclusionId": "opaque"}),
		"POST /api/v1/version-operations/{id}/undo":                   admin(webhookParam),
		"GET /api/v1/items/{id}/track-preferences":                    {mode: leakByID, params: itemParam, control: true},
		"PUT /api/v1/items/{id}/track-preferences":                    {mode: leakByID, params: itemParam, control: true},
		"GET /api/v1/users/me/track-preferences":                      noMedia(noParams, "caller's own default track preferences; languages and modes only"),
		"PUT /api/v1/users/me/track-preferences":                      exempt("replaces the caller's own default track preferences; carries no media identifiers"),
		// Watch statistics (G23.3, G48.3): the daily roll-up read with the
		// viewer's library grants; the fixture seeds rows for both users on
		// both items.
		// Collections and playlists (G02.1, G48.3). The fixture's mixed
		// collection and the viewer's public playlist hold the visible and
		// the hidden item; a second collection holds only the hidden item, so
		// its name is a marker the viewer's listing must not show. Changes
		// name items in the body and answer hidden items like missing ones
		// (TestCollectionsAndPlaylistsHTTPPostgres).
		"GET /api/v1/collections":                            {mode: leakList, params: noParams, control: true},
		"POST /api/v1/collections":                           admin(noParams),
		"POST /api/v1/collections/nfo-sync":                  admin(noParams),
		"GET /api/v1/collections/{id}":                       {mode: leakList, params: map[string]string{"id": "collection"}, control: true},
		"PUT /api/v1/collections/{id}":                       admin(map[string]string{"id": "collection"}),
		"DELETE /api/v1/collections/{id}":                    admin(map[string]string{"id": "collection"}),
		"POST /api/v1/collections/{id}/items":                admin(map[string]string{"id": "collection"}),
		"DELETE /api/v1/collections/{id}/items/{itemId}":     admin(map[string]string{"id": "collection", "itemId": "item"}),
		"GET /api/v1/playlists":                              {mode: leakList, params: noParams, control: true},
		"POST /api/v1/playlists":                             exempt("creates a playlist of the caller; carries no media identifiers"),
		"GET /api/v1/playlists/{id}":                         {mode: leakList, params: map[string]string{"id": "playlist"}, control: true},
		"PUT /api/v1/playlists/{id}":                         exempt("renames the caller's own playlist; the response lists only entries visible to the caller like GET (TestCollectionsAndPlaylistsHTTPPostgres)"),
		"DELETE /api/v1/playlists/{id}":                      exempt("deletes the caller's own playlist; returns an empty 204"),
		"POST /api/v1/playlists/{id}/items":                  exempt("appends items named in the body; hidden items are answered like missing ones (TestCollectionsAndPlaylistsHTTPPostgres)"),
		"DELETE /api/v1/playlists/{id}/entries/{entryId}":    exempt("removes an entry of the caller's own playlist; the response lists only visible entries (TestCollectionsAndPlaylistsHTTPPostgres)"),
		"POST /api/v1/playlists/{id}/entries/{entryId}/move": exempt("reorders the caller's own playlist; the response lists only visible entries (TestCollectionsAndPlaylistsHTTPPostgres)"),
		"GET /api/v1/users/me/watch-stats":                   {mode: leakList, params: noParams, control: true},
		"GET /api/v1/users/{id}/watch-stats":                 admin(selfParam),
		"GET /api/v1/watch-stats":                            admin(noParams),
		"GET /api/v1/watch-stats/export":                     admin(noParams),
		"GET /images/{type}/{id}":                            {mode: leakByID, params: map[string]string{"type": "image-type", "id": "item"}, control: true},
		"HEAD /images/{type}/{id}":                           {mode: leakByID, params: map[string]string{"type": "image-type", "id": "item"}, control: true},

		// Accounts.
		"POST /api/v1/auth/login":                        exempt("credential exchange; takes no media identifiers and returns only a session grant"),
		"POST /api/v1/auth/login/native":                 exempt("native credential exchange; takes no media identifiers and returns only a session grant"),
		"POST /api/v1/auth/logout":                       exempt("revokes the caller's own session; carries no media identifiers"),
		"POST /api/v1/auth/rotate":                       exempt("rotates the caller's own token; carries no media identifiers"),
		"GET /api/v1/auth/csrf":                          noMedia(noParams, "returns only a token derived from the caller's own credential"),
		"PUT /api/v1/users/me/profile":                   exempt("mutates the caller's own profile; carries no media identifiers"),
		"PUT /api/v1/users/me/password":                  exempt("mutates the caller's own password; carries no media identifiers"),
		"PUT /api/v1/users/me/preferences":               exempt("replaces the caller's own interface preferences; carries no media identifiers"),
		"GET /api/v1/users/me/preferences":               noMedia(noParams, "caller's own interface preferences"),
		"DELETE /api/v1/users/{id}/sessions":             exempt("revokes the caller's own sessions; carries no media identifiers"),
		"DELETE /api/v1/users/{id}/sessions/{sessionID}": exempt("revokes one of the caller's sessions; carries no media identifiers"),
		"GET /api/v1/users/me":                           noMedia(noParams, "caller's own account"),
		"GET /api/v1/users/{id}":                         noMedia(selfParam, "caller's own account"),
		"GET /api/v1/users/{id}/sessions":                noMedia(selfParam, "caller's own sessions"),
		"GET /api/v1/users/{id}/libraries":               {mode: leakList, params: selfParam},
		"GET /api/v1/users":                              admin(noParams),
		"POST /api/v1/users":                             admin(noParams),
		"PUT /api/v1/users/{id}":                         admin(selfParam),
		"DELETE /api/v1/users/{id}":                      admin(selfParam),
		"POST /api/v1/users/{id}/restore":                admin(selfParam),
		"POST /api/v1/users/{id}/unlock":                 admin(selfParam),
		"PUT /api/v1/users/{id}/native":                  admin(selfParam),
		"GET /api/v1/users/{id}/delivery-limits":         admin(selfParam),
		"PUT /api/v1/users/{id}/delivery-limits":         admin(selfParam),
		"GET /api/v1/sessions":                           admin(noParams),
		"PUT /api/v1/users/{id}/libraries":               admin(selfParam),
		// Second factor and application passwords (G07.8): credentials of the
		// caller's own account; no media identifiers in or out.
		"POST /api/v1/auth/login/second-factor":                   exempt("second login step; takes a challenge and a code, returns only a session grant"),
		"GET /api/v1/users/{id}/two-factor":                       noMedia(selfParam, "caller's own second factor state"),
		"DELETE /api/v1/users/{id}/two-factor":                    admin(selfParam),
		"POST /api/v1/users/me/two-factor/enroll":                 exempt("starts the caller's own enrollment; carries no media identifiers"),
		"POST /api/v1/users/me/two-factor/confirm":                exempt("confirms the caller's own enrollment; carries no media identifiers"),
		"POST /api/v1/users/me/two-factor/recovery-codes":         exempt("replaces the caller's own recovery codes; carries no media identifiers"),
		"POST /api/v1/users/me/two-factor/disable":                exempt("disables the caller's own second factor; carries no media identifiers"),
		"GET /api/v1/users/{id}/app-passwords":                    noMedia(selfParam, "caller's own application password labels"),
		"POST /api/v1/users/me/app-passwords":                     exempt("creates the caller's own application password; carries no media identifiers"),
		"DELETE /api/v1/users/{id}/app-passwords/{appPasswordId}": exempt("revokes one of the caller's application passwords; carries no media identifiers"),
		"GET /api/v1/users/{id}/data-export":                      noMedia(selfParam, "caller's own personal data export; names items by ID only"),
		"POST /api/v1/users/me/purge":                             exempt("permanently deletes the caller's own account; carries no media identifiers"),
		"POST /api/v1/users/{id}/purge":                           admin(selfParam),
		// Content access administration (G48.1, G48.4).
		"GET /api/v1/users/{id}/content-access":                   admin(selfParam),
		"PUT /api/v1/users/{id}/content-access":                   admin(selfParam),
		"PUT /api/v1/users/{id}/content-access/items/{itemId}":    admin(map[string]string{"id": "self", "itemId": "item"}),
		"DELETE /api/v1/users/{id}/content-access/items/{itemId}": admin(map[string]string{"id": "self", "itemId": "item"}),
		"GET /api/v1/access/policy":                               admin(noParams),
		// Client control administration (G47): rules, policy, hits and known
		// clients name clients, never media.
		"GET /api/v1/client-control/policy":              admin(noParams),
		"PUT /api/v1/client-control/policy":              admin(noParams),
		"GET /api/v1/client-control/rules":               admin(noParams),
		"POST /api/v1/client-control/rules":              admin(noParams),
		"GET /api/v1/client-control/rules/{id}":          admin(webhookParam),
		"PUT /api/v1/client-control/rules/{id}":          admin(webhookParam),
		"DELETE /api/v1/client-control/rules/{id}":       admin(webhookParam),
		"POST /api/v1/client-control/rules/{id}/enforce": admin(webhookParam),
		"POST /api/v1/client-control/rules/{id}/observe": admin(webhookParam),
		"GET /api/v1/client-control/hits":                admin(noParams),
		"GET /api/v1/client-control/hits/export":         admin(noParams),
		"GET /api/v1/client-control/stats":               admin(noParams),
		"GET /api/v1/client-control/clients":             admin(noParams),
		"PATCH /api/v1/client-control/clients/{id}":      admin(webhookParam),
		"POST /api/v1/client-control/clients/{id}/block": admin(webhookParam),
		"POST /api/v1/client-control/clients/{id}/kick":  admin(webhookParam),
		"PUT /api/v1/access/policy":                      admin(noParams),
		"GET /api/v1/access/parental-ratings":            admin(noParams),
		// G48.5 network rules and G48.6 share links.
		"GET /api/v1/access/network-rules":                admin(noParams),
		"POST /api/v1/access/network-rules":               admin(noParams),
		"PUT /api/v1/access/network-rules/{id}":           admin(shareParam),
		"DELETE /api/v1/access/network-rules/{id}":        admin(shareParam),
		"GET /api/v1/shares":                              admin(noParams),
		"POST /api/v1/shares":                             admin(noParams),
		"GET /api/v1/shares/{id}":                         admin(shareParam),
		"POST /api/v1/shares/{id}/revoke":                 admin(shareParam),
		"GET /api/v1/shares/{id}/access":                  admin(shareParam),
		"GET /api/v1/shares/current":                      noMedia(noParams, "the caller's own share; 404 for any session that is not a guest"),
		"POST /api/v1/shares/redeem":                      exempt("share token exchange; takes no media identifiers and returns only a guest session grant"),
		"POST /api/v1/shares/redeem/native":               exempt("native share token exchange; takes no media identifiers and returns only a guest session grant"),
		"GET /api/v1/site/appearance":                     noMedia(noParams, "site-wide appearance with sanitized CSS; no media or administrator data"),
		"GET /api/v1/site/plugins":                        noMedia(noParams, "site-wide plugin order and settings; no media or administrator data"),
		"GET /api/v1/site/appearance/config":              admin(noParams),
		"PUT /api/v1/site/appearance":                     admin(noParams),
		"POST /api/v1/site/appearance/reset":              admin(noParams),
		"GET /api/v1/site/plugins/config":                 admin(noParams),
		"PUT /api/v1/site/plugins":                        admin(noParams),
		"POST /api/v1/site/plugins/reset":                 admin(noParams),
		"GET /api/v1/site/export":                         admin(noParams),
		"POST /api/v1/site/import":                        admin(noParams),
		"GET /metrics":                                    admin(noParams),
		"GET /api/v1/items/{id}/metadata":                 admin(itemParam),
		"PUT /api/v1/items/{id}/metadata":                 admin(itemParam),
		"POST /api/v1/items/{id}/metadata/nfo":            admin(itemParam),
		"POST /api/v1/items/{id}/metadata/tmdb":           admin(itemParam),
		"DELETE /api/v1/items/{id}/metadata/external":     admin(itemParam),
		"GET /api/v1/libraries/{id}/metadata-preferences": admin(libParam),
		"PUT /api/v1/libraries/{id}/metadata-preferences": admin(libParam),

		// TMDB lookups are administrator-only and keyed by provider IDs.
		"GET /api/v1/metadata/tmdb/movies":                                          admin(noParams),
		"GET /api/v1/metadata/tmdb/movies/{id}":                                     admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/movies/{id}/images":                              admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/series":                                          admin(noParams),
		"GET /api/v1/metadata/tmdb/series/{id}":                                     admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/series/{id}/images":                              admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/series/{id}/seasons/{season}":                    admin(map[string]string{"id": "tmdb", "season": "tmdb"}),
		"GET /api/v1/metadata/tmdb/series/{id}/seasons/{season}/episodes/{episode}": admin(map[string]string{"id": "tmdb", "season": "tmdb", "episode": "tmdb"}),

		// Library administration and jobs.
		"GET /api/v1/libraries":                    admin(noParams),
		"GET /api/v1/libraries/{id}/watch":         admin(libParam),
		"GET /api/v1/libraries/{id}/schedule":      admin(libParam),
		"PUT /api/v1/libraries/{id}/schedule":      admin(libParam),
		"POST /api/v1/libraries/{id}/schedule/run": admin(libParam),
		"POST /api/v1/libraries/{id}/scan":         admin(libParam),
		// G50.4 repair actions: administrator only; the run ID is opaque.
		"POST /api/v1/admin/repairs":                                                admin(noParams),
		"POST /api/v1/admin/repairs/{id}/revert":                                    admin(webhookParam),
		"GET /api/v1/libraries/{id}/catalog-sync":                                   admin(libParam),
		"PUT /api/v1/libraries/{id}/catalog-sync":                                   admin(libParam),
		"POST /api/v1/libraries/{id}/catalog-sync":                                  admin(libParam),
		"GET /api/v1/libraries/{id}/catalog-sync/pending":                           admin(libParam),
		"POST /api/v1/libraries/{id}/probe/rebuild":                                 admin(libParam),
		"POST /api/v1/items/{id}/probe/rebuild":                                     admin(itemParam),
		"GET /api/v1/libraries/{id}/nfo/policy":                                     admin(libParam),
		"PUT /api/v1/libraries/{id}/nfo/policy":                                     admin(libParam),
		"POST /api/v1/libraries/{id}/nfo/validate":                                  admin(libParam),
		"GET /api/v1/libraries/{id}/nfo/current-validations":                        admin(libParam),
		"GET /api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues": admin(map[string]string{"id": "library", "observationId": "opaque"}),
		"GET /api/v1/jobs":                                                          admin(noParams),
		"GET /api/v1/jobs/{id}":                                                     admin(jobParam),
		"GET /api/v1/jobs/{id}/entries":                                             admin(jobParam),
		"GET /api/v1/jobs/{id}/catalog-sync":                                        admin(jobParam),
		"POST /api/v1/jobs/{id}/accept-missing":                                     admin(jobParam),
		"PUT /api/v1/jobs/{id}/entries/{entry}/item":                                admin(map[string]string{"id": "job", "entry": "opaque"}),
		"GET /api/v1/jobs/{id}/imports":                                             admin(jobParam),
		"POST /api/v1/jobs/{id}/imports":                                            admin(jobParam),
		"GET /api/v1/jobs/{id}/ignore":                                              admin(jobParam),
		"GET /api/v1/jobs/{id}/probe":                                               admin(jobParam),
		"GET /api/v1/jobs/{id}/nfo":                                                 admin(jobParam),
		"GET /api/v1/jobs/{id}/images":                                              admin(jobParam),
		"POST /api/v1/jobs/{id}/cancel":                                             admin(jobParam),
		"POST /api/v1/jobs/{id}/retry":                                              admin(jobParam),

		// Webhook administration (G12) carries no media identifiers and is
		// administrator-only; viewers are refused before any lookup.
		"GET /api/v1/webhooks":                                      admin(noParams),
		"POST /api/v1/webhooks":                                     admin(noParams),
		"GET /api/v1/webhooks/{id}":                                 admin(webhookParam),
		"PUT /api/v1/webhooks/{id}":                                 admin(webhookParam),
		"DELETE /api/v1/webhooks/{id}":                              admin(webhookParam),
		"POST /api/v1/webhooks/{id}/rotate-secret":                  admin(webhookParam),
		"POST /api/v1/webhooks/{id}/test":                           admin(webhookParam),
		"GET /api/v1/webhooks/{id}/deliveries":                      admin(webhookParam),
		"GET /api/v1/webhooks/{id}/deliveries/{deliveryId}":         admin(map[string]string{"id": "opaque", "deliveryId": "opaque"}),
		"POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/replay": admin(map[string]string{"id": "opaque", "deliveryId": "opaque"}),
	}
}

var leakPathParam = regexp.MustCompile(`\{([^}]+)\}`)

func leakWalk(t *testing.T, handler http.Handler) map[string]bool {
	t.Helper()
	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose chi routes")
	}
	seen := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return seen
}

// leakConfig enables every rollout flag so the walk sees every route.
func leakConfig(t *testing.T, dsn string, hiddenStatus int) config.Config {
	t.Helper()
	cfg := validConfig()
	cfg.DatabaseURL = dsn
	cfg.MaxConnections, cfg.MaxStreams, cfg.RequestTimeoutSeconds = 16, 4, 10
	cfg.EnableCatalog, cfg.EnableDirect, cfg.EnableAccounts, cfg.EnableMetrics, cfg.EnableImages, cfg.EnableJobs = true, true, true, true, true, true
	cfg.Accounts, cfg.Jobs, cfg.Images = config.DefaultAccountsConfig(), config.DefaultJobsConfig(), config.DefaultImagesConfig()
	cfg.Images.TempRoot = t.TempDir()
	cfg.TMDBAPIKey = strings.Repeat("a", 32)
	cfg.Access.HiddenStatus = hiddenStatus
	cfg.EnableCompat = true
	cfg.EnableWebhooks, cfg.Webhooks = true, config.DefaultWebhooksConfig()
	cfg.Webhooks.MasterKey = testWebhookMasterKey
	// A developer capable instance with no active session, so the
	// developer routes are walked too.
	cfg.Dev = config.DevConfig{EnvFlag: true, Enabled: true}
	cfg.Matroska = config.MatroskaConfig{EnableExtraction: true, CacheRoot: filepath.Clean(t.TempDir()), CacheMaxBytes: 1 << 30}
	cfg.SubtitleOCR = config.DefaultSubtitleOCRConfig()
	cfg.SubtitleOCR.Enable, cfg.SubtitleOCR.CacheRoot = true, filepath.Clean(t.TempDir())
	return cfg
}

// leakExtracted authorizes through the store's source lookup, like the
// runtime extraction resolver, and serves one fixed cached file.
type leakExtracted struct {
	store *postgres.Store
	root  string
}

func (l leakExtracted) ResolveExtracted(ctx context.Context, p access.Principal, sourceID string, _ media.ExtractedKind, _ int) (media.Source, error) {
	source, err := l.store.Resolve(ctx, p, sourceID)
	if err != nil {
		return media.Source{}, err
	}
	return media.Source{Root: l.root, RelativePath: "extracted.srt", DeviceID: source.DeviceID, Limits: source.Limits, ShareStreams: source.ShareStreams}, nil
}

func newLeakExtracted(t *testing.T, store *postgres.Store) leakExtracted {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extracted.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\nextracted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return leakExtracted{store: store, root: root}
}

type leakRenderer struct{}

func (leakRenderer) Render(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
	data := []byte{0xff, 0xd8, 0xff, 0xd9}
	return app.ImageResult{Body: &httpImageBody{Reader: bytes.NewReader(data)}, ContentType: "image/jpeg", ETag: httpImageETag, Size: int64(len(data)), Width: 1, Height: 1}, nil
}

func (r leakRenderer) RenderItemImage(ctx context.Context, _ domain.ItemImage, request domain.ImageRequest) (app.ImageResult, error) {
	return r.Render(ctx, domain.LocalImageSource{}, request)
}

func leakHandler(t *testing.T, store *postgres.Store, cfg config.Config) http.Handler {
	t.Helper()
	return leakHandlerWith(t, store, cfg, &httpAccountPasswords{})
}

func leakHandlerWith(t *testing.T, store *postgres.Store, cfg config.Config, passwords *httpAccountPasswords) http.Handler {
	t.Helper()
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return leakHandlerWithProgress(t, store, cfg, passwords, progress)
}

// leakHandlerWithProgress is leakHandlerWith with the caller's progress
// buffer, so a test can flush it.
func leakHandlerWithProgress(t *testing.T, store *postgres.Store, cfg config.Config, passwords *httpAccountPasswords, progress *app.Progress) http.Handler {
	t.Helper()
	return leakHandlerWithRenderer(t, store, cfg, passwords, progress, leakRenderer{})
}

// leakImageRenderer renders local posters and item_images rows.
type leakImageRenderer interface {
	app.ImageRenderer
	app.ItemImageRenderer
}

// leakHandlerWithRenderer is leakHandlerWithProgress with the caller's
// image renderer.
func leakHandlerWithRenderer(t *testing.T, store *postgres.Store, cfg config.Config, passwords *httpAccountPasswords, progress *app.Progress, renderer leakImageRenderer) http.Handler {
	t.Helper()
	return leakHandlerWithAccounts(t, store, cfg, passwords, progress, renderer, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
}

// leakHandlerWithAccounts is leakHandlerWithRenderer with the caller's
// account options, such as a second factor key and clock.
func leakHandlerWithAccounts(t *testing.T, store *postgres.Store, cfg config.Config, passwords *httpAccountPasswords, progress *app.Progress, renderer leakImageRenderer, accountOptions app.AccountOptions) http.Handler {
	t.Helper()
	accounts, err := app.NewAccounts(store, passwords, accountOptions)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := app.NewJobs(store, cfg.Jobs.Policy())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.NewMetadata(&httpMovieProvider{})
	if err == nil {
		metadata, err = metadata.WithLibraryPreferences(store)
	}
	if err == nil {
		metadata, err = metadata.WithItemMetadata(store)
	}
	if err != nil {
		t.Fatal(err)
	}
	images, err := app.NewImages(store, renderer)
	if err == nil {
		images, err = images.WithAssets(store, renderer)
	}
	if err == nil {
		images, err = images.WithSummaries(store)
	}
	if err != nil {
		t.Fatal(err)
	}
	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "# metrics\n") })
	catalog, err := app.NewCatalog(store).WithPlayback(store)
	if err == nil {
		catalog, err = catalog.WithBrowse(store)
	}
	if err == nil {
		catalog, err = catalog.WithDetails(store)
	}
	if err == nil {
		catalog, err = catalog.WithVersions(store)
	}
	if err == nil {
		catalog, err = catalog.WithCollections(store)
	}
	if err != nil {
		t.Fatal(err)
	}
	if catalog, err = catalog.WithProgress(progress); err != nil {
		t.Fatal(err)
	}
	stats, err := app.NewWatchStats(store, app.WatchStatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if catalog, err = catalog.WithWatchStats(stats); err != nil {
		t.Fatal(err)
	}
	repairer, err := app.NewRepairer(store, httpNoFiles{}, nil, httpPaths{})
	if err != nil {
		t.Fatal(err)
	}
	options := []Option{WithWebhooks(httpWebhooks(t, store)), WithSetup(completedSetupWizard(), ""), WithExtracted(newLeakExtracted(t, store)), WithRepair(repairer.WithServer(jobs, nil), app.RepairOptions{Policy: cfg.Jobs.Policy()})}
	if cfg.Dev.Capable() {
		// The developer routes exist but no session is active (G45.8).
		dev, err := devmode.NewController(devmode.ControllerOptions{Store: store, Local: cfg.Dev.Inputs()})
		if err != nil {
			t.Fatal(err)
		}
		options = append(options, WithDevMode(dev))
	}
	if store.Pool != nil {
		// The traversal runs behind the client control gate with no rules,
		// as production does by default.
		options = append(options, WithClientControl(httpClientControl(t, store)))
	}
	handler, err := NewWithImages(cfg, store, catalog, store, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs, metadata, metrics, images, options...)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestAccessLeakRouteTableIsComplete(t *testing.T) {
	// Route registration never touches the pool, so an unconnected store is enough.
	handler := leakHandler(t, &postgres.Store{}, leakConfig(t, "postgres://localhost/jelee", 0))
	seen := leakWalk(t, handler)
	table := leakRouteTable()
	var problems []string
	for route := range seen {
		spec, ok := table[route]
		if !ok {
			problems = append(problems, route+": registered but not classified in leakRouteTable; add a leak check or an exemption with a reason")
			continue
		}
		method, pattern, _ := strings.Cut(route, " ")
		switch spec.mode {
		case leakExempt:
			if strings.TrimSpace(spec.reason) == "" {
				problems = append(problems, route+": exemption needs a reason")
			}
			continue
		case leakNoMedia:
			if strings.TrimSpace(spec.reason) == "" || (method != http.MethodGet && method != http.MethodHead) {
				problems = append(problems, route+": no-media checks need a reason and a safe method")
			}
		case leakByID:
			media := false
			for _, kind := range spec.params {
				media = media || kind == "item" || kind == "source" || kind == "library"
			}
			if !media {
				problems = append(problems, route+": by-ID check needs a media parameter")
			}
		case leakList, leakAdmin:
		default:
			problems = append(problems, route+": unknown mode")
		}
		names := leakPathParam.FindAllStringSubmatch(pattern, -1)
		if len(names) != len(spec.params) {
			problems = append(problems, route+": parameter table does not match the pattern")
		}
		for _, name := range names {
			if _, ok := spec.params[name[1]]; !ok {
				problems = append(problems, route+": parameter "+name[1]+" has no fixture kind")
			}
		}
	}
	for route := range table {
		if !seen[route] {
			problems = append(problems, route+": classified but no longer registered; remove the stale entry")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	// Default construction keeps the 404 semantics.
	if (config.Config{}).Access.HiddenContentStatus() != http.StatusNotFound {
		t.Fatal("hidden content default changed")
	}
}

type leakIDs struct {
	viewer, adminToken, viewerToken string
	item, source, library, job      [3]string // visible, hidden, missing
	// collection holds the visible and the hidden item; playlist is the
	// viewer's public playlist of both (G02.1).
	collection, playlist string
	subtitle, audio      [3]string // sidecar tracks of the sources above
	markers              []string
	// itemMarkers are the item-level markers of the hidden fixture, for
	// mechanisms that hide the item inside a granted library.
	itemMarkers                 []string
	visibleItem, visibleLibrary string
	admin                       domain.Actor
	// seedProgress restores the resume points listing routes expect; the
	// played routes change them.
	seedProgress func(*testing.T)
	// progressUsers are the users seedProgress seeds.
	progressUsers *[]string
	// guest marks a viewer that is a share guest (G48.6): routes outside
	// the guest routes must refuse it without a marker.
	guest bool
}

const (
	leakVisible = iota
	leakHidden
	leakMissing
)

func (f leakIDs) value(kind string, scenario int) string {
	switch kind {
	case "item":
		return f.item[scenario]
	case "source":
		return f.source[scenario]
	case "library":
		return f.library[scenario]
	case "job":
		return f.job[scenario]
	case "subtitle-track":
		return f.subtitle[scenario]
	case "audio-track":
		return f.audio[scenario]
	case "self":
		return f.viewer
	case "collection":
		return f.collection
	case "playlist":
		return f.playlist
	case "image-type":
		return "Primary"
	case "tmdb":
		return "12"
	case "opaque":
		return "00000000-0000-4000-8000-000000000001"
	case "compat-container":
		// Every fixture source is a Matroska file.
		return "mkv"
	case "compat-subtitle-index":
		// The fixture sources are unprobed, so their only external
		// subtitle is the first stream.
		return "0"
	case "compat-subtitle-format":
		return "srt"
	case "compat-zero":
		return "0"
	case "attachment-id":
		return "1"
	}
	panic("unknown leak fixture kind " + kind)
}

// leakWire is the dashless identifier form the compatibility layer writes.
func leakWire(id string) string { return strings.ReplaceAll(id, "-", "") }

// leakContainsAny reports whether text holds any of the identifiers in
// either form.
func leakContainsAny(text string, ids ...string) bool {
	for _, id := range ids {
		if strings.Contains(text, id) || strings.Contains(text, leakWire(id)) {
			return true
		}
	}
	return false
}

func leakUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// leakStore owns a fresh random schema in the dedicated jelee_test database.
func leakStore(t *testing.T) (context.Context, *postgres.Store, string) {
	t.Helper()
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required access leak PostgreSQL integration is unavailable")
		}
		t.Skip("access leak PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("access leak integration requires the dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated access leak test database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal("generate schema identifier")
	}
	schema := "jelee_leak_it_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create owned access leak schema")
	}
	t.Cleanup(func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := admin.Exec(cleanCtx, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error("remove owned access leak schema")
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	if version, dirty, e := postgres.Migrate(ctx, u.String(), "up"); e != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate access leak fixture to current schema")
	}
	store, err := postgres.Open(ctx, u.String(), 16)
	if err != nil {
		t.Fatal("open access leak test store")
	}
	t.Cleanup(store.Pool.Close)
	return ctx, store, u.String()
}

func leakFixture(t *testing.T, ctx context.Context, store *postgres.Store) leakIDs {
	t.Helper()
	var f leakIDs
	var err error
	if f.adminToken, err = store.Provision(ctx, "leak-admin", access.ClientNative, true); err != nil {
		t.Fatal(err)
	}
	if f.viewerToken, err = store.Provision(ctx, "leak-viewer", access.ClientNative, false); err != nil {
		t.Fatal(err)
	}
	var adminID, adminSession string
	if err = store.Pool.QueryRow(ctx, `SELECT u.id::text,s.id::text FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.name='leak-admin'`).Scan(&adminID, &adminSession); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(ctx, `SELECT id::text FROM users WHERE name='leak-viewer'`).Scan(&f.viewer); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	for scenario, spec := range map[int]struct{ library, root, title, file string }{
		leakVisible: {"Visible Leak Library", "visible-root", "Visible Leak Probe Title", "visible-file.mkv"},
		leakHidden:  {"Hidden Leak Library Qx7", "hidden-secret-root-Qx7", "Hidden Leak Probe Title Qx7", "hidden-secret-file-Qx7.mkv"},
	} {
		root := filepath.Join(base, spec.root)
		if err = os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, spec.file), []byte("Jelee synthetic leak probe\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var rootID string
		if err = store.Pool.QueryRow(ctx, `INSERT INTO libraries(name) VALUES($1) RETURNING id::text`, spec.library).Scan(&f.library[scenario]); err != nil {
			t.Fatal(err)
		}
		if err = store.Pool.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, f.library[scenario], root).Scan(&rootID); err != nil {
			t.Fatal(err)
		}
		if err = store.Pool.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, f.library[scenario], spec.title).Scan(&f.item[scenario]); err != nil {
			t.Fatal(err)
		}
		if err = store.Pool.QueryRow(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`, f.item[scenario], f.library[scenario], rootID, spec.file).Scan(&f.source[scenario]); err != nil {
			t.Fatal(err)
		}
		// An asset row exercises the item_images resolver on the image routes.
		if _, err = store.Pool.Exec(ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,'Primary',0,'local',$3::uuid,$4)`, f.item[scenario], f.library[scenario], rootID, strings.TrimSuffix(spec.file, ".mkv")+"-poster.jpg"); err != nil {
			t.Fatal(err)
		}
		// External tracks exercise the sidecar resolver on the track routes.
		base := strings.TrimSuffix(spec.file, ".mkv")
		for _, track := range []struct {
			slot       *[3]string
			name, kind string
			format     string
		}{{&f.subtitle, base + ".en.srt", "subtitle", "srt"}, {&f.audio, base + ".en.ac3", "audio", "ac3"}} {
			if err = os.WriteFile(filepath.Join(root, track.name), []byte("Jelee synthetic leak track\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err = store.Pool.QueryRow(ctx, `INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,size,modified_unix_nano) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,27,0) RETURNING id::text`, f.source[scenario], f.library[scenario], rootID, track.name, track.kind, track.format).Scan(&track.slot[scenario]); err != nil {
				t.Fatal(err)
			}
		}
		job, _, err := store.SubmitJob(ctx, domain.Actor{UserID: adminID, SessionID: adminSession, IP: "127.0.0.1"}, f.library[scenario], "leak-"+spec.root, domain.JobPriorityManual, config.DefaultJobsConfig().Policy())
		if err != nil {
			t.Fatal(err)
		}
		f.job[scenario] = job.ID
		if scenario == leakHidden {
			f.markers = []string{f.item[scenario], f.source[scenario], f.library[scenario], rootID, job.ID, spec.library, spec.root, spec.title, spec.file, root,
				f.subtitle[scenario], f.audio[scenario], base + ".en.srt", base + ".en.ac3"}
			f.itemMarkers = []string{f.item[scenario], f.source[scenario], spec.title, spec.file, f.subtitle[scenario], f.audio[scenario], base + ".en.srt", base + ".en.ac3"}
			// The compatibility layer writes identifiers without dashes.
			for _, id := range []string{f.item[scenario], f.source[scenario], f.library[scenario], rootID, f.subtitle[scenario], f.audio[scenario]} {
				f.markers = append(f.markers, leakWire(id))
			}
			for _, id := range []string{f.item[scenario], f.source[scenario], f.subtitle[scenario], f.audio[scenario]} {
				f.itemMarkers = append(f.itemMarkers, leakWire(id))
			}
		} else {
			f.visibleItem, f.visibleLibrary = f.item[scenario], f.library[scenario]
		}
	}
	f.admin = domain.Actor{UserID: adminID, SessionID: adminSession, IP: "127.0.0.1"}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer, f.library[leakVisible]); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []*[3]string{&f.item, &f.source, &f.library, &f.job, &f.subtitle, &f.audio} {
		slot[leakMissing] = leakUUID(t)
	}
	// Collections and playlists (G02.1): the hidden item sorts and plays
	// first, so the administrator control's cover is the hidden item.
	if err = store.Pool.QueryRow(ctx, `INSERT INTO collections(name) VALUES('Leak Mixed Collection') RETURNING id::text`).Scan(&f.collection); err != nil {
		t.Fatal(err)
	}
	hiddenOnly := "Hidden Only Collection Qx7"
	if _, err = store.Pool.Exec(ctx, `WITH c AS (INSERT INTO collections(name) VALUES($3) RETURNING id)
 INSERT INTO collection_items(collection_id,item_id) SELECT $1::uuid,unnest($2::uuid[]) UNION ALL SELECT c.id,$4::uuid FROM c`,
		f.collection, []string{f.item[leakVisible], f.item[leakHidden]}, hiddenOnly, f.item[leakHidden]); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(ctx, `INSERT INTO playlists(owner_id,name,public) VALUES($1::uuid,'Leak Viewer Playlist',true) RETURNING id::text`, f.viewer).Scan(&f.playlist); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO playlist_items(playlist_id,item_id,position) VALUES($1::uuid,$2::uuid,0),($1::uuid,$3::uuid,1)`, f.playlist, f.item[leakHidden], f.item[leakVisible]); err != nil {
		t.Fatal(err)
	}
	f.markers = append(f.markers, hiddenOnly)
	f.itemMarkers = append(f.itemMarkers, hiddenOnly)
	// Both users have a resume point on both items, so the continue
	// watching lists must filter the viewer's hidden one (G48.3) while the
	// administrator control lists it.
	progressUsers := []string{f.viewer, adminID}
	f.progressUsers = &progressUsers
	f.seedProgress = func(t *testing.T) {
		t.Helper()
		if _, err := store.Pool.Exec(ctx, `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,updated_at)
 SELECT u,i,6000000000,false,0,now(),now() FROM unnest($1::uuid[]) u CROSS JOIN unnest($2::uuid[]) i
 ON CONFLICT (user_id,item_id) DO UPDATE SET resume_ticks=EXCLUDED.resume_ticks,played=false,play_count=0`,
			*f.progressUsers, []string{f.item[leakVisible], f.item[leakHidden]}); err != nil {
			t.Fatal(err)
		}
		// Watch statistics of today on both items for both users.
		if _, err := store.Pool.Exec(ctx, `INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays)
 SELECT u,(now() AT TIME ZONE 'UTC')::date,i.id,i.library_id,600000,1,1,1 FROM unnest($1::uuid[]) u CROSS JOIN items i WHERE i.id=ANY($2::uuid[])
 ON CONFLICT (user_id,day,item_id) DO NOTHING`, *f.progressUsers, []string{f.item[leakVisible], f.item[leakHidden]}); err != nil {
			t.Fatal(err)
		}
	}
	f.seedProgress(t)
	return f
}

type leakResponse struct {
	status int
	code   string
	text   string
}

func leakRequest(t *testing.T, handler http.Handler, method, path, token string) leakResponse {
	t.Helper()
	var body io.Reader
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodDelete {
		body = strings.NewReader("{}")
	}
	r := httptest.NewRequest(method, "http://localhost"+path, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Idempotency-Key", "leak-probe")
	if strings.HasPrefix(path, "/compat/") {
		// The compatibility layer does not accept bearer tokens. Streams
		// are asked for as originals.
		r.URL.RawQuery = url.Values{"ApiKey": {token}, "static": {"true"}}.Encode()
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	var text strings.Builder
	text.WriteString(w.Body.String())
	for name, values := range w.Header() {
		fmt.Fprintf(&text, "\n%s: %s", name, strings.Join(values, ","))
	}
	return leakResponse{status: w.Code, code: envelope.Error.Code, text: text.String()}
}

func leakPath(pattern string, spec leakRoute, f leakIDs, scenario int) string {
	return leakPathParam.ReplaceAllStringFunc(pattern, func(m string) string {
		return f.value(spec.params[m[1:len(m)-1]], scenario)
	})
}

func (f leakIDs) assertNoMarkers(t *testing.T, route string, response leakResponse) {
	t.Helper()
	for _, marker := range f.markers {
		if strings.Contains(response.text, marker) {
			t.Errorf("%s: response leaks hidden marker %q (status %d)", route, marker, response.status)
		}
	}
}

// leakMechanisms are the ways the fixture's hidden item is hidden from the
// viewer (G48.1, G48.4). Every mechanism goes through the unified filter, so
// the same traversal must find zero leaks for each.
var leakMechanisms = []string{"library_grant", "item_rule", "parental_rating", "blocked_tag", "network_rule", "client_restrict_libraries", "share_scope"}

// leakHideBy hides the fixture's hidden item from the viewer by mechanism.
// Except for the library grant, the viewer is granted the hidden library,
// so only the item-level markers must stay out of responses.
func leakHideBy(t *testing.T, ctx context.Context, store *postgres.Store, f *leakIDs, mechanism string) {
	t.Helper()
	switch mechanism {
	case "library_grant":
		return
	case "network_rule", "client_restrict_libraries":
		// The viewer is granted the hidden library, which only the request
		// restriction hides (G48.5): the whole library stays out.
		if _, err := store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer, f.library[leakHidden]); err != nil {
			t.Fatal(err)
		}
		var err error
		if mechanism == "network_rule" {
			// Test requests come from 192.0.2.1, outside any LAN.
			_, err = store.CreateNetworkRule(ctx, f.admin, domain.NetworkRuleInput{LibraryID: f.library[leakHidden], Network: "lan", CIDRs: []string{}, ClientKinds: []string{}, Enabled: true})
		} else {
			_, err = store.CreateClientRule(ctx, f.admin, domain.ClientRuleInput{Dimension: "ip", Match: "cidr", Pattern: "0.0.0.0/0", Action: "restrict_libraries",
				Libraries: []string{f.library[leakVisible]}, ScopeKind: "user", ScopeValues: []string{f.viewer}, Enabled: true})
		}
		if err != nil {
			t.Fatalf("hide by %s: %v", mechanism, err)
		}
		return
	case "share_scope":
		// The viewer is a native guest of a share of the visible library
		// (G48.6); the hidden library is outside the share.
		g, err := store.CreateShare(ctx, f.admin, domain.ShareInput{LibraryID: f.library[leakVisible], ExpiresAt: time.Now().Add(time.Hour), AllowPlayback: true, MaxStreams: 16})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := store.RedeemShare(ctx, domain.ShareRedemption{Token: g.Token, Native: true, Client: domain.NativeClient{Name: "leak guest", DeviceID: "leak-guest"}, MaxSessions: 8, SessionTTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		f.viewer, f.viewerToken, f.guest = grant.User.ID, grant.Token, true
		*f.progressUsers = append(*f.progressUsers, grant.User.ID)
		f.seedProgress(t)
		return
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer, f.library[leakHidden]); err != nil {
		t.Fatal(err)
	}
	f.markers = f.itemMarkers
	var err error
	switch mechanism {
	case "item_rule":
		_, err = store.SetItemAccessRule(ctx, f.admin, f.viewer, f.item[leakHidden], domain.ItemAccessHide)
	case "parental_rating":
		rated := "NC-17"
		if _, err = store.UpdateItemMetadata(ctx, f.admin, f.item[leakHidden], 1, []domain.ItemMetadataPatch{{Field: "mpaa", Value: &rated}}); err == nil {
			ceiling := 13
			_, err = store.SetContentAccess(ctx, f.admin, f.viewer, domain.ContentAccess{ParentalRatingMax: &ceiling, BlockedTags: []string{}})
		}
	case "blocked_tag":
		if _, err = store.UpdateItemMetadataWithFacts(ctx, f.admin, f.item[leakHidden], 1, nil, []domain.ItemMetadataFactPatch{{Field: "tags", Value: json.RawMessage(`["Leak Blocked"]`)}}); err == nil {
			_, err = store.SetContentAccess(ctx, f.admin, f.viewer, domain.ContentAccess{BlockedTags: []string{"leak blocked"}})
		}
	default:
		t.Fatalf("unknown mechanism %s", mechanism)
	}
	if err != nil {
		t.Fatalf("hide by %s: %v", mechanism, err)
	}
}

func TestAccessLeakHiddenContentPostgres(t *testing.T) {
	for _, mechanism := range leakMechanisms {
		t.Run(mechanism, func(t *testing.T) {
			ctx, store, dsn := leakStore(t)
			f := leakFixture(t, ctx, store)
			leakHideBy(t, ctx, store, &f, mechanism)
			leakTraverse(t, store, dsn, f)
		})
	}
}

// leakTraverse requests every registered route in the three hidden status
// modes and asserts zero leaks of f's hidden markers.
func leakTraverse(t *testing.T, store *postgres.Store, dsn string, f leakIDs) {
	t.Helper()
	for _, mode := range []struct {
		name   string
		status int
		code   string
	}{{"default_404", 0, "not_found"}, {"explicit_404", http.StatusNotFound, "not_found"}, {"configured_403", http.StatusForbidden, "forbidden"}} {
		t.Run(mode.name, func(t *testing.T) {
			cfg := leakConfig(t, dsn, mode.status)
			handler := leakHandler(t, store, cfg)
			want := cfg.Access.HiddenContentStatus()
			table := leakRouteTable()
			routes := make([]string, 0, len(table))
			for route := range leakWalk(t, handler) {
				routes = append(routes, route)
			}
			sort.Strings(routes)
			checked := 0
			for _, route := range routes {
				spec, ok := table[route]
				if !ok {
					t.Errorf("%s: registered but not classified", route)
					continue
				}
				method, pattern, _ := strings.Cut(route, " ")
				if _, allowed := guestRoutes[route]; f.guest && spec.mode != leakExempt && spec.mode != leakNoMedia && !allowed {
					// Outside the guest routes a guest is refused before any
					// lookup: 403 share_forbidden, or 401 from the
					// compatibility layer, which refuses guests. Routes
					// without media (public ones among them) are scanned
					// for markers below like for any viewer.
					response := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.viewerToken)
					refused := response.status == http.StatusForbidden && response.code == "share_forbidden" ||
						strings.HasPrefix(pattern, "/compat/") && response.status == http.StatusUnauthorized
					if !refused {
						t.Errorf("%s: guest got %d/%s, want a refusal", route, response.status, response.code)
					}
					f.assertNoMarkers(t, route, response)
					checked++
					continue
				}
				switch spec.mode {
				case leakExempt:
					continue
				case leakNoMedia:
					response := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.viewerToken)
					if response.status >= 500 {
						t.Errorf("%s: no-media route failed with %d", route, response.status)
					}
					f.assertNoMarkers(t, route, response)
				case leakList:
					f.seedProgress(t)
					path := leakPath(pattern, spec, f, leakHidden)
					response := leakRequest(t, handler, method, path, f.viewerToken)
					if response.status != http.StatusOK || !leakContainsAny(response.text, f.visibleItem, f.visibleLibrary) {
						t.Errorf("%s: viewer listing lost visible content (status %d)", route, response.status)
					}
					f.assertNoMarkers(t, route, response)
					if spec.control {
						// A library folder listing names the hidden library
						// rather than its item.
						if control := leakRequest(t, handler, method, path, f.adminToken); control.status != http.StatusOK || !leakContainsAny(control.text, f.item[leakHidden], f.library[leakHidden]) {
							t.Errorf("%s: administrator control does not see the hidden fixture (status %d)", route, control.status)
						}
					}
				case leakAdmin:
					hidden := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.viewerToken)
					missing := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakMissing), f.viewerToken)
					if hidden.status != http.StatusForbidden || hidden.code != "forbidden" || missing.status != hidden.status || missing.code != hidden.code {
						t.Errorf("%s: administrator route answered a viewer with hidden=%d/%s missing=%d/%s", route, hidden.status, hidden.code, missing.status, missing.code)
					}
					f.assertNoMarkers(t, route, hidden)
				case leakByID:
					visible := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakVisible), f.viewerToken)
					if visible.status != http.StatusOK {
						t.Errorf("%s: visible control returned %d/%s", route, visible.status, visible.code)
					}
					hidden := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.viewerToken)
					missing := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakMissing), f.viewerToken)
					// The compatibility layer answers with an empty body,
					// so it has no error code to compare.
					code := mode.code
					if strings.HasPrefix(pattern, "/compat/") {
						code = ""
					}
					if hidden.status != want || missing.status != want || (method != http.MethodHead && (hidden.code != code || missing.code != code)) {
						t.Errorf("%s: hidden=%d/%s missing=%d/%s, want %d/%s for both", route, hidden.status, hidden.code, missing.status, missing.code, want, mode.code)
					}
					f.assertNoMarkers(t, route, hidden)
					f.assertNoMarkers(t, route, missing)
					if spec.control {
						if control := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.adminToken); control.status != http.StatusOK {
							t.Errorf("%s: administrator control cannot reach the hidden fixture (%d/%s)", route, control.status, control.code)
						}
					}
				}
				checked++
			}
			if checked < 50 {
				t.Fatalf("only %d routes were exercised", checked)
			}
			t.Logf("access leak traversal exercised %d routes with hidden status %d", checked, want)
		})
	}
}
