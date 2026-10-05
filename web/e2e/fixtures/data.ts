// Fixed data for the end-to-end and visual tests. Every value is typed by the
// generated API contract (src/api/schema.d.ts), so a contract change that
// would make these answers impossible fails `npm run types`. Text is ASCII
// only: screenshots must not depend on which CJK fonts a machine has.
import type { components } from "../../src/api/schema";

type Schemas = components["schemas"];

/** Every timestamp the fake server reports; the browser clock is fixed to it too. */
export const now = "2026-10-01T12:00:00Z";
const day = (offset: number) => new Date(Date.parse(now) + offset * 86_400_000).toISOString().replace(".000Z", "Z");
const id = (n: number) => "00000000-0000-4000-8000-" + String(n).padStart(12, "0");

export const ids = {
  admin: id(1),
  session: id(2),
  movies: id(10),
  shows: id(11),
  alice: id(20),
  bob: id(21),
  webhook: id(30),
  rule: id(40),
  client: id(50),
  share: id(60),
} as const;

export const adminUser: Schemas["User"] = {
  id: ids.admin,
  name: "admin",
  displayName: "Avery Admin",
  locale: "en-US",
  hidden: false,
  admin: true,
  disabled: false,
  allowNative: true,
  createdAt: day(-90),
};

export const users: Schemas["User"][] = [
  adminUser,
  { id: ids.alice, name: "alice", displayName: "Alice", locale: "en-US", hidden: false, admin: false, disabled: false, allowNative: true, createdAt: day(-40) },
  { id: ids.bob, name: "bob", displayName: "Bob", locale: "en-US", hidden: false, admin: false, disabled: true, allowNative: false, createdAt: day(-12) },
];

export const libraries: Schemas["LibrarySummary"][] = [
  { id: ids.movies, name: "Movies", roots: 2 },
  { id: ids.shows, name: "Shows", roots: 1 },
];

const movieTitles = [
  "Arrival at Dawn",
  "Blue Harbor",
  "Copper Valley",
  "Distant Signals",
  "Echo Lake",
  "Field Notes",
  "Glass Orchard",
  "Harvest Moon",
  "Iron Coast",
  "Juniper Street",
  "Kite Season",
  "Lantern Hill",
];

const movies: Schemas["CatalogItem"][] = movieTitles.map((title, index) => ({
  id: id(100 + index),
  kind: "Movie" as const,
  libraryId: ids.movies,
  title,
  productionYear: 2010 + index,
  premiereDate: String(2010 + index) + "-0" + String((index % 9) + 1) + "-15",
}));

/** The item every detail-page test opens. */
export const detailItem: Schemas["CatalogItem"] = movies[0] ?? { id: id(100), kind: "Movie", libraryId: ids.movies, title: "" };

export const items: Schemas["CatalogItem"][] = [
  ...movies,
  { id: id(200), kind: "Series", libraryId: ids.shows, title: "Northern Lights", productionYear: 2021 },
  { id: id(201), kind: "Series", libraryId: ids.shows, title: "Quiet Rooms", productionYear: 2023 },
];

export const itemDetails: Schemas["CatalogItemDetails"] = {
  ...detailItem,
  originalTitle: "Arrival at Dawn",
  sortTitle: "Arrival at Dawn",
  tagline: "Every morning starts somewhere.",
  overview:
    "A lighthouse keeper on a remote island keeps a logbook of the ships that pass at dawn, until one morning a ship arrives that is not on any chart.",
  genres: ["Drama", "Mystery"],
  externalIds: [
    { type: "tmdb", value: "424242", default: true },
    { type: "imdb", value: "tt0424242", default: false },
  ],
  nfo: { status: "valid", fields: ["title", "overview", "genres", "year"], readAt: day(-3) },
};

export const itemSources: Schemas["MediaSourceInfo"][] = [
  {
    id: id(300),
    primary: true,
    probed: true,
    container: "mkv",
    contentType: "video/x-matroska",
    durationMicros: 6_840_000_000,
    sizeBytes: 4_812_000_000,
    bitRate: 5_600_000,
    version: { displayName: "1080p", qualityScore: 80 },
    videoTracks: [{ index: 0, codec: "h264", profile: "High", level: 41, width: 1920, height: 1080, frameRate: { numerator: 24000, denominator: 1001 }, default: true, primary: true }],
    audioTracks: [{ index: 1, codec: "eac3", channels: 6, channelLayout: "5.1", language: "eng", default: true, forced: false, atmos: false, sampleRate: 48000 }],
    subtitleTracks: [{ index: 2, codec: "subrip", language: "eng", default: false, forced: false }],
    externalTracks: [],
  },
];

export const trackPreferences: Schemas["TrackPreferences"] = { itemId: detailItem.id, item: null, user: null, versions: [] };

export const versionOverview: Schemas["VersionOverview"] = { itemId: detailItem.id, primarySourceId: id(300), exclusions: [], operations: [] };

export const preferences: Schemas["UserPreferences"] = { theme: "system", density: "comfortable", layout: null };

export const sessions: Schemas["Session"][] = [
  { id: ids.session, userId: ids.admin, clientKind: "web", deviceName: "Jelee Web", createdAt: day(-1), expiresAt: day(6), lastSeenAt: day(0), lastIp: "192.0.2.10" },
  { id: id(3), userId: ids.admin, clientKind: "native", client: "Living Room", deviceName: "TV", version: "10.10.0", createdAt: day(-5), expiresAt: day(25), lastSeenAt: day(-2), lastIp: "192.0.2.24" },
];

export const twoFactor: Schemas["TwoFactorStatus"] = { available: true, enabled: false, pending: false, recoveryCodesRemaining: 0 };

const statsRow = (views: number) => ({
  views,
  sessions: views + 2,
  completions: Math.floor(views / 2),
  completionRate: 0.5,
  firstPlays: Math.floor(views / 3),
  rewatches: 1,
  effectiveSeconds: views * 2400,
});

export const watchStats: Schemas["WatchStatsReport"] = {
  period: "week",
  from: "2026-09-01",
  to: "2026-09-28",
  timeZone: "UTC",
  weekStart: "monday",
  totals: statsRow(42),
  periods: ["2026-09-01", "2026-09-08", "2026-09-15", "2026-09-22"].map((start, index) => ({ start, ...statsRow(6 + index * 4) })),
  kinds: [
    { kind: "Movie", ...statsRow(30) },
    { kind: "Episode", ...statsRow(12) },
  ],
  libraries: [
    { libraryId: ids.movies, name: "Movies", ...statsRow(30) },
    { libraryId: ids.shows, name: "Shows", ...statsRow(12) },
  ],
  topItems: items.slice(0, 3).map((item, index) => ({ itemId: item.id, title: item.title, kind: item.kind, libraryId: item.libraryId, ...statsRow(9 - index * 2) })),
  topUsers: [
    { userId: ids.admin, userName: "admin", ...statsRow(25) },
    { userId: ids.alice, userName: "alice", ...statsRow(17) },
  ],
};

export const accessPolicy: Schemas["AccessPolicy"] = { blockUnrated: false, restrictAdmins: false };

export const parentalRatings: Schemas["ParentalRating"][] = [
  { code: "G", level: 0 },
  { code: "PG", level: 7 },
  { code: "PG-13", level: 13 },
  { code: "R", level: 17 },
];

export const networkRules: Schemas["NetworkRule"][] = [
  { id: id(41), libraryId: ids.movies, libraryName: "Movies", network: "lan", cidrs: [], clientKinds: ["web", "native"], includeAdmins: false, enabled: true, note: "Home network only", createdAt: day(-20), updatedAt: day(-20) },
];

export const clientPolicy: Schemas["ClientPolicy"] = { unknownClients: "allow", exemptAdmins: true, exemptLoopback: true, version: 3, updatedAt: day(-7) };

export const clientRules: Schemas["ClientRule"][] = [
  {
    id: ids.rule,
    priority: 10,
    dimension: "user_agent",
    match: "prefix",
    pattern: "OldPlayer/",
    caseFold: true,
    action: "deny",
    scopeKind: "global",
    scopeValues: [],
    enabled: true,
    note: "Retired client",
    hitCount: 14,
    lastHitAt: day(-1),
    createdAt: day(-30),
    updatedAt: day(-30),
  },
];

export const knownClients: Schemas["KnownClient"][] = [
  {
    id: ids.client,
    clientKind: "native",
    appName: "Living Room",
    appVersion: "10.10.0",
    deviceName: "TV",
    deviceId: "tv-01",
    lastIp: "192.0.2.24",
    activeSessions: 1,
    blocked: false,
    trusted: true,
    firstSeenAt: day(-60),
    lastSeenAt: day(-2),
    lastUserId: ids.admin,
  },
];

export const clientHits: Schemas["ClientHit"][] = [
  { id: 1, bucket: day(-1), ruleId: ids.rule, action: "deny", mode: "enforced", surface: "compat", userAgent: "OldPlayer/1.0", hits: 14 },
];

export const clientStats: Schemas["ClientHitStats"] = {
  since: day(-7),
  total: 14,
  blocked: 14,
  observed: 0,
  byAction: [{ value: "deny", hits: 14 }],
  topRules: [{ value: ids.rule, hits: 14 }],
  topIps: [{ value: "198.51.100.7", hits: 14 }],
  topUserAgents: [{ value: "OldPlayer/1.0", hits: 14 }],
};

export const webhooks: Schemas["Webhook"][] = [
  {
    id: ids.webhook,
    name: "Chat notifications",
    url: "https://hooks.example.com/jelee",
    events: ["media.added", "scan.completed"],
    enabled: true,
    headerNames: [],
    timeoutSeconds: 10,
    retry: { maxAttempts: 5, baseDelaySeconds: 10, maxDelaySeconds: 600, jitter: 0.2 },
    pending: 0,
    dead: 1,
    createdAt: day(-14),
    updatedAt: day(-14),
  },
];

export const webhookEvents: Schemas["Webhook"]["events"] = ["media.added", "media.updated", "media.deleted", "scan.started", "scan.completed", "scan.failed"];

export const webhookDeliveries: Schemas["WebhookDelivery"][] = [
  { id: id(31), webhookId: ids.webhook, eventId: "evt-1", eventType: "scan.completed", state: "delivered", attempts: 1, replays: 0, lastOutcome: "delivered", lastStatus: 204, occurredAt: day(-1), createdAt: day(-1), lastAttemptAt: day(-1) },
  { id: id(32), webhookId: ids.webhook, eventId: "evt-2", eventType: "media.added", state: "dead", attempts: 5, replays: 0, lastOutcome: "timeout", occurredAt: day(-2), createdAt: day(-2), lastAttemptAt: day(-2) },
];

export const shares: Schemas["Share"][] = [
  {
    id: ids.share,
    libraryId: ids.movies,
    libraryName: "Movies",
    itemId: detailItem.id,
    itemTitle: detailItem.title,
    itemKind: "Movie",
    note: "For the book club",
    readOnly: true,
    allowPlayback: false,
    maxStreams: 1,
    activeSessions: 0,
    state: "active",
    createdAt: day(-3),
    expiresAt: day(4),
  },
];

export const appearance: Schemas["SiteAppearanceConfig"] = {
  defaultTheme: "system",
  tokens: { light: {}, dark: {} },
  customCss: "",
  allowExternalFonts: false,
  fontHosts: [],
  defaultLayout: null,
  revision: 1,
  updatedAt: day(-10),
  cssIssues: [],
};

export const sitePlugins: Schemas["SitePluginsConfig"] = { plugins: [], settings: {}, revision: 1, updatedAt: day(-10) };

export const setupState: Schemas["SetupState"] = {
  version: 1,
  current: "language",
  admin: {},
  database: { schemaVersion: 80 },
  metadataPolicy: { imageFetch: true, imageWriteBack: false },
  network: { privacyAcknowledged: false },
  tmdb: { enabled: false },
  toolchain: { acceptDegraded: false, available: ["ffprobe"], missing: [] },
};
