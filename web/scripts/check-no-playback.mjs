#!/usr/bin/env node
// Build-time gate for G27 / G35.5: the web client must never regain media
// playback. Fails when a playback package appears in any manifest or the
// lockfile, or when sources, catalogs or the built bundle contain playback
// elements, playback APIs or playback routes.
//
// Usage: node scripts/check-no-playback.mjs [--require-dist] [--root <repo>]
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

/** Packages that provide players, streaming engines or casting. */
export const bannedPackages = [
  "hls.js",
  "dashjs",
  "shaka-player",
  "video.js",
  "plyr",
  "mpegts.js",
  "flv.js",
  "media-chrome",
  "vidstack",
  "artplayer",
  "xgplayer",
  "dplayer",
  "clappr",
  "jwplayer",
  "react-player",
  "vue-video-player",
  "vue3-video-play",
  "castjs",
  "mux-embed",
];

/** Package name prefixes reserved for player ecosystems. */
export const bannedPackagePrefixes = ["@videojs/", "videojs-", "@vidstack/", "@mux/", "@clappr/", "@silvermine/videojs"];

/** Source-level markers of playback, media sessions, PiP or casting. */
export const bannedSourcePatterns = [
  { name: "<video> element", pattern: /<video\b/i },
  { name: "<audio> element", pattern: /<audio\b/i },
  { name: "MediaSource API", pattern: /\bMediaSource\b|ManagedMediaSource/ },
  { name: "media element types", pattern: /\bHTML(?:Media|Video|Audio)Element\b/ },
  { name: "created media element", pattern: /createElement\(\s*["'`](?:video|audio)["'`]/i },
  { name: "Picture-in-Picture", pattern: /requestPictureInPicture|pictureInPictureElement/ },
  { name: "Media Session API", pattern: /\bmediaSession\b|\bMediaMetadata\b/ },
  { name: "remote playback / casting", pattern: /\bRemotePlayback\b|\bPresentationRequest\b|chrome\.cast/ },
  { name: "Encrypted Media Extensions", pattern: /requestMediaKeySystemAccess/ },
];

/**
 * Path segments that would denote a playback route, the stream endpoint or
 * the external subtitle and audio track, the extracted embedded subtitle and
 * font attachment and the OCR-derived subtitle delivery endpoints.
 */
const playbackSegment =
  /^(?:play|player|playback|playing|now-playing|stream|streams|subtitles|embedded-subtitles|ocr-subtitles|attachments|audio|cast|pip|picture-in-picture|theater)$/i;
const pathLiteral = /["'`](\/[A-Za-z0-9_:{}()*./-]*)["'`]/g;

const sourceExtensions = new Set([".ts", ".vue", ".js", ".mjs", ".html", ".css"]);

function walk(directory, files = []) {
  if (!existsSync(directory)) {
    return files;
  }
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.name === "node_modules") {
      continue;
    }
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      walk(path, files);
    } else if (entry.isFile()) {
      files.push(path);
    }
  }
  return files;
}

function extension(path) {
  const dot = path.lastIndexOf(".");
  return dot < 0 ? "" : path.slice(dot);
}

export function isBannedPackage(name) {
  return bannedPackages.includes(name) || bannedPackagePrefixes.some((prefix) => name.startsWith(prefix));
}

/** Returns banned dependency names declared by a package.json object. */
export function manifestViolations(manifest) {
  /** @type {string[]} */
  const found = [];
  for (const field of ["dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "bundleDependencies", "bundledDependencies"]) {
    const value = manifest[field];
    const names = Array.isArray(value) ? value : Object.keys(value ?? {});
    for (const name of names) {
      if (isBannedPackage(name)) {
        found.push(field + ": " + name);
      }
    }
  }
  return found;
}

/** Returns banned packages anywhere in an npm v3 lockfile, at any depth. */
export function lockfileViolations(lock) {
  /** @type {string[]} */
  const found = [];
  for (const [key, entry] of Object.entries(lock.packages ?? {})) {
    const marker = key.lastIndexOf("node_modules/");
    const name = marker < 0 ? (entry?.name ?? "") : key.slice(marker + "node_modules/".length);
    if (name && isBannedPackage(name)) {
      found.push(key);
    }
    for (const dependency of Object.keys({ ...(entry?.dependencies ?? {}), ...(entry?.optionalDependencies ?? {}) })) {
      if (isBannedPackage(dependency)) {
        found.push(key + " -> " + dependency);
      }
    }
  }
  return found;
}

/** Scans one text for playback markers; checkRoutes adds path-literal checks. */
export function textViolations(text, { checkRoutes = true } = {}) {
  /** @type {string[]} */
  const found = [];
  for (const { name, pattern } of bannedSourcePatterns) {
    if (pattern.test(text)) {
      found.push(name);
    }
  }
  if (checkRoutes) {
    for (const match of text.matchAll(pathLiteral)) {
      const path = match[1] ?? "";
      if (path.split("/").some((segment) => playbackSegment.test(segment))) {
        found.push("playback route or stream path " + path);
      }
    }
  }
  return found;
}

/** Catalog keys must not carry playback vocabulary (G27.1 translation keys). */
export function catalogViolations(catalog, prefix = "") {
  /** @type {string[]} */
  const found = [];
  for (const [key, value] of Object.entries(catalog)) {
    const path = prefix ? prefix + "." + key : key;
    if (/play|stream|pictureInPicture|cast/i.test(key)) {
      found.push("catalog key " + path);
    }
    if (typeof value === "object" && value !== null) {
      found.push(...catalogViolations(value, path));
    }
  }
  return found;
}

export function checkWorkspace(repoRoot, { requireDist = false } = {}) {
  const web = join(repoRoot, "web");
  /** @type {string[]} */
  const problems = [];
  const report = (file, items) => {
    for (const item of items) {
      problems.push(relative(repoRoot, file).split(sep).join("/") + ": " + item);
    }
  };

  for (const file of [join(repoRoot, "package.json"), join(web, "package.json")]) {
    if (existsSync(file)) {
      report(file, manifestViolations(JSON.parse(readFileSync(file, "utf8"))));
    }
  }
  for (const file of [join(repoRoot, "package-lock.json"), join(web, "package-lock.json")]) {
    if (existsSync(file)) {
      report(file, lockfileViolations(JSON.parse(readFileSync(file, "utf8"))));
    }
  }

  const sources = [join(web, "index.html"), ...walk(join(web, "src"))];
  for (const file of sources) {
    // Tests assert the absence of these markers and never ship; the dist
    // scan covers everything that does.
    if (!existsSync(file) || /\.test\.ts$/.test(file) || file.includes(join("src", "test") + sep)) {
      continue;
    }
    if (file.endsWith(".json")) {
      report(file, catalogViolations(JSON.parse(readFileSync(file, "utf8"))));
      continue;
    }
    if (!sourceExtensions.has(extension(file)) && !file.endsWith(".d.ts")) {
      continue;
    }
    // The generated contract lists every server path, including the native
    // stream and track endpoints; api/client.ts removes them from the web
    // client's type.
    const generated = file.endsWith(join("src", "api", "schema.d.ts"));
    report(file, textViolations(readFileSync(file, "utf8"), { checkRoutes: !generated }));
  }

  const dist = join(web, "dist");
  if (existsSync(dist) && statSync(dist).isDirectory()) {
    const built = walk(dist).filter((file) => sourceExtensions.has(extension(file)));
    if (requireDist && built.length === 0) {
      problems.push("web/dist: no built files to check");
    }
    for (const file of built) {
      report(file, textViolations(readFileSync(file, "utf8"), { checkRoutes: false }));
    }
  } else if (requireDist) {
    problems.push("web/dist: missing; run the build first");
  }
  return { problems, scanned: sources.length };
}

function main(argv) {
  const rootIndex = argv.indexOf("--root");
  const repoRoot = rootIndex >= 0 && argv[rootIndex + 1] ? resolve(argv[rootIndex + 1]) : fileURLToPath(new URL("../..", import.meta.url));
  const { problems, scanned } = checkWorkspace(repoRoot, { requireDist: argv.includes("--require-dist") });
  if (problems.length > 0) {
    console.error("no-playback gate failed (G27/G35.5):");
    for (const problem of problems) {
      console.error("  " + problem);
    }
    return 1;
  }
  console.log("no-playback gate: manifests, lockfile, " + scanned + " source files and dist are free of playback");
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
