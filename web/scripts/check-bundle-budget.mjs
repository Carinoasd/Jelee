#!/usr/bin/env node
// Bundle budget gate (G35.4). After `vite build`, measures what the browser
// must download before the first page can run: the entry script, every
// module it preloads, and the initial stylesheets referenced by
// dist/index.html, each compressed with gzip level 9. Fails when any total
// exceeds web/bundle-budget.json. Raising a budget is a reviewed change to
// that file, with the reason recorded in its "note".
//
// Usage: node scripts/check-bundle-budget.mjs [--dist <dir>] [--budget <file>]
import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

/** Metrics the budget file may limit, in gzip bytes. */
export const budgetKeys = ["entryJsGzipBytes", "initialJsGzipBytes", "initialCssGzipBytes"];

function gzipSize(path) {
  return gzipSync(readFileSync(path), { level: 9 }).length;
}

/** Asset paths index.html loads up front: entry scripts, module preloads, stylesheets. */
export function initialAssets(html) {
  const attribute = (tag, name) => new RegExp(`\\b${name}\\s*=\\s*["']([^"']+)["']`, "i").exec(tag)?.[1] ?? null;
  const entries = [];
  const preloads = [];
  const styles = [];
  for (const [tag] of html.matchAll(/<script\b[^>]*>/gi)) {
    const src = attribute(tag, "src");
    if (src !== null && /\btype\s*=\s*["']module["']/i.test(tag)) {
      entries.push(src);
    }
  }
  for (const [tag] of html.matchAll(/<link\b[^>]*>/gi)) {
    const rel = (attribute(tag, "rel") ?? "").toLowerCase();
    const href = attribute(tag, "href");
    if (href === null) {
      continue;
    }
    if (rel === "modulepreload") {
      preloads.push(href);
    } else if (rel === "stylesheet") {
      styles.push(href);
    }
  }
  return { entries, preloads, styles };
}

/** Measures the initial download of a built dist directory. */
export function measureBundle(dist) {
  const indexPath = join(dist, "index.html");
  if (!existsSync(indexPath)) {
    throw new Error(dist + ": index.html missing; run the build first");
  }
  const { entries, preloads, styles } = initialAssets(readFileSync(indexPath, "utf8"));
  if (entries.length === 0) {
    throw new Error(indexPath + ": no module entry script");
  }
  const size = (href) => {
    // Built URLs are absolute from the site root ("/assets/x.js").
    const path = join(dist, href.replace(/^\/+/, "").split(/[?#]/)[0]);
    if (!existsSync(path)) {
      throw new Error(href + ": referenced by index.html but missing from " + dist);
    }
    return { href, gzip: gzipSize(path) };
  };
  const entryFiles = entries.map(size);
  const preloadFiles = preloads.map(size);
  const styleFiles = styles.map(size);
  const sum = (files) => files.reduce((total, file) => total + file.gzip, 0);
  return {
    files: [...entryFiles, ...preloadFiles, ...styleFiles],
    metrics: {
      entryJsGzipBytes: sum(entryFiles),
      initialJsGzipBytes: sum(entryFiles) + sum(preloadFiles),
      initialCssGzipBytes: sum(styleFiles),
    },
  };
}

/** Parses and validates a budget object; throws on anything unexpected. */
export function parseBudget(value) {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("budget must be a JSON object");
  }
  const budget = {};
  for (const key of budgetKeys) {
    const limit = value[key];
    if (!Number.isInteger(limit) || limit <= 0) {
      throw new Error("budget." + key + " must be a positive integer");
    }
    budget[key] = limit;
  }
  for (const key of Object.keys(value)) {
    if (!budgetKeys.includes(key) && key !== "note") {
      throw new Error("budget: unknown field " + key);
    }
  }
  return budget;
}

/** Compares measured metrics with a budget; returns the violations. */
export function compareBudget(metrics, budget) {
  return budgetKeys
    .filter((key) => metrics[key] > budget[key])
    .map((key) => key + ": " + metrics[key] + " B gzip exceeds the budget of " + budget[key] + " B by " + (metrics[key] - budget[key]) + " B");
}

export function checkBudget(dist, budgetFile) {
  const budget = parseBudget(JSON.parse(readFileSync(budgetFile, "utf8")));
  const measured = measureBundle(dist);
  return { ...measured, budget, problems: compareBudget(measured.metrics, budget) };
}

function main(argv) {
  const webRoot = fileURLToPath(new URL("..", import.meta.url));
  const option = (name, fallback) => {
    const index = argv.indexOf(name);
    return index >= 0 && argv[index + 1] ? resolve(argv[index + 1]) : fallback;
  };
  const dist = option("--dist", join(webRoot, "dist"));
  const budgetFile = option("--budget", join(webRoot, "bundle-budget.json"));
  let result;
  try {
    result = checkBudget(dist, budgetFile);
  } catch (error) {
    console.error("bundle budget gate failed (G35.4): " + (error instanceof Error ? error.message : String(error)));
    return 1;
  }
  for (const key of budgetKeys) {
    const percent = ((result.metrics[key] / result.budget[key]) * 100).toFixed(1);
    console.log("bundle budget: " + key + " " + result.metrics[key] + " / " + result.budget[key] + " B (" + percent + "%)");
  }
  if (result.problems.length > 0) {
    console.error("bundle budget gate failed (G35.4):");
    for (const problem of result.problems) {
      console.error("  " + problem);
    }
    return 1;
  }
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
