#!/usr/bin/env node
// i18n gate (G03.4): exactly the four locales, identical namespace files and
// keys, strict JSON without duplicate keys, nonempty strings, matching
// placeholders, no Simplified/Traditional mixing, every referenced key exists
// and every catalog key is used by the application.
//
// Layout: web/src/i18n/<locale>/<namespace>.json holding one top-level key
// equal to <namespace>. The reserved core.json namespace (server UI strings
// moved from the legacy catalogs) may be flat and is exempt from the
// unused-key rule because the web code does not reference those strings yet.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const locales = ["zh-CN", "zh-TW", "ja-JP", "en-US"];
export const referenceLocale = "en-US";
export const reservedNamespace = "core";

/** Strict JSON parser that rejects duplicate object keys. */
export function parseStrictJSON(text) {
  let index = 0;
  const fail = (message) => {
    throw new SyntaxError(message + " at offset " + index);
  };
  const space = () => {
    while (index < text.length && " \t\n\r".includes(text[index])) {
      index++;
    }
  };
  const string = () => {
    const start = index;
    index++;
    while (index < text.length && text[index] !== '"') {
      index += text[index] === "\\" ? 2 : 1;
    }
    if (index >= text.length) {
      fail("unterminated string");
    }
    index++;
    return JSON.parse(text.slice(start, index));
  };
  const value = () => {
    space();
    const char = text[index];
    if (char === "{") {
      index++;
      const result = {};
      space();
      if (text[index] === "}") {
        index++;
        return result;
      }
      for (;;) {
        space();
        if (text[index] !== '"') {
          fail("expected key");
        }
        const key = string();
        if (Object.hasOwn(result, key)) {
          fail("duplicate key " + JSON.stringify(key));
        }
        space();
        if (text[index] !== ":") {
          fail("expected ':'");
        }
        index++;
        result[key] = value();
        space();
        if (text[index] === ",") {
          index++;
          continue;
        }
        if (text[index] === "}") {
          index++;
          return result;
        }
        fail("expected ',' or '}'");
      }
    }
    if (char === "[") {
      const start = index;
      let depth = 0;
      do {
        if (text[index] === '"') {
          string();
          continue;
        }
        if (text[index] === "[") depth++;
        if (text[index] === "]") depth--;
        index++;
      } while (depth > 0 && index < text.length);
      return JSON.parse(text.slice(start, index));
    }
    if (char === '"') {
      return string();
    }
    const match = /^(?:true|false|null|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(text.slice(index));
    if (!match) {
      fail("unexpected token");
    }
    index += match[0].length;
    return JSON.parse(match[0]);
  };
  const result = value();
  space();
  if (index !== text.length) {
    fail("trailing content");
  }
  return result;
}

/** Flattens nested messages to dotted keys; values must be nonempty strings. */
export function flatten(object, prefix, out = new Map(), problems = []) {
  for (const [key, value] of Object.entries(object)) {
    const path = prefix ? prefix + "." + key : key;
    if (key.includes(".") || key.length === 0) {
      problems.push(path + ": invalid key segment");
    } else if (typeof value === "string") {
      if (value.trim().length === 0) {
        problems.push(path + ": empty message");
      }
      out.set(path, value);
    } else if (typeof value === "object" && value !== null && !Array.isArray(value)) {
      flatten(value, path, out, problems);
    } else {
      problems.push(path + ": expected string or object");
    }
  }
  return out;
}

/** Named and list placeholders, e.g. {name} and {0}; linked messages @:key. */
export function placeholders(message) {
  const found = [...message.matchAll(/\{\s*([A-Za-z0-9_]+)\s*\}|@:[A-Za-z0-9_.]+/g)].map((match) => match[1] ?? match[0]);
  return found.sort().join(",");
}

// Characters that exist only in one script; a pair never appears in the
// other catalog except in self-names of languages (common.localeNames.*).
const scriptPairs = "简簡 体體 语語 载載 录錄 户戶 权權 访訪 览覽 务務 设設 请請 试試 导導 编編 号號 码碼 页頁 网網 连連 为為 无無 库庫 败敗 时時 间間 们們 达達 说說 这這 个個 门門 动動 态態 应應 话話 观觀 视視 词詞 认認 证證 数數 线線 将將 显顯 从從 删刪 际際 过過 还還 开開 关關 听聽 发發 转轉 选選 级級 处處".split(" ");
const simplifiedOnly = new Set(scriptPairs.map((pair) => pair[0]).filter((char, i) => char !== scriptPairs[i][1]));
const traditionalOnly = new Set(scriptPairs.map((pair) => pair[1]).filter((char, i) => char !== scriptPairs[i][0]));

export function scriptMixing(locale, key, message) {
  if (key.startsWith("common.localeNames.")) {
    return null;
  }
  const foreign = locale === "zh-CN" ? traditionalOnly : locale === "zh-TW" ? simplifiedOnly : null;
  if (foreign === null) {
    return null;
  }
  const hit = [...message].find((char) => foreign.has(char));
  return hit ? key + ": contains " + hit + " from the other Chinese script" : null;
}

function sourceFiles(directory, files = []) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      sourceFiles(path, files);
    } else if (/\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith(".d.ts") && !entry.name.endsWith(".test.ts")) {
      files.push(path);
    }
  }
  return files;
}

/** Collects dotted string literals in a namespace that sources reference. */
export function referencedKeys(sources, namespaces) {
  const used = new Set();
  const literal = /["'`]([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)+)["'`]/g;
  for (const source of sources) {
    // Template attributes holding a plain property path (v-if="auth.user")
    // are expressions, not message keys.
    const text = source.replace(/(?<=[\w:@#.-])="[A-Za-z_$][\w$]*(?:\.[\w$]+)+"/g, "");
    for (const match of text.matchAll(literal)) {
      const key = match[1] ?? "";
      // File names such as "core.json" are not message keys.
      if (/\.(?:json|ts|vue|css)$/.test(key)) {
        continue;
      }
      if (namespaces.has(key.split(".")[0] ?? "")) {
        used.add(key);
      }
    }
  }
  return used;
}

export function checkCatalogs(webRoot) {
  const i18nRoot = join(webRoot, "src", "i18n");
  /** @type {string[]} */
  const problems = [];
  const directories = readdirSync(i18nRoot, { withFileTypes: true }).filter((entry) => entry.isDirectory()).map((entry) => entry.name).sort();
  if (directories.join() !== [...locales].sort().join()) {
    problems.push("locale directories must be exactly " + locales.join(", ") + "; found " + directories.join(", "));
    return { problems, keys: 0 };
  }
  const catalogs = new Map();
  let namespaceList = null;
  for (const locale of locales) {
    const files = readdirSync(join(i18nRoot, locale)).filter((name) => name.endsWith(".json")).sort();
    const others = readdirSync(join(i18nRoot, locale)).filter((name) => !name.endsWith(".json"));
    if (others.length > 0) {
      problems.push(locale + ": unexpected files " + others.join(", "));
    }
    if (namespaceList === null) {
      namespaceList = files;
    } else if (files.join() !== namespaceList.join()) {
      problems.push(locale + ": namespace files differ: " + files.join(", ") + " vs " + namespaceList.join(", "));
    }
    const flat = new Map();
    for (const file of files) {
      const namespace = file.slice(0, -".json".length);
      let parsed;
      try {
        parsed = parseStrictJSON(readFileSync(join(i18nRoot, locale, file), "utf8"));
      } catch (error) {
        problems.push(locale + "/" + file + ": " + String(error instanceof Error ? error.message : error));
        continue;
      }
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
        problems.push(locale + "/" + file + ": expected an object");
        continue;
      }
      const keys = Object.keys(parsed);
      let body = parsed;
      if (keys.length === 1 && keys[0] === namespace) {
        body = parsed[namespace];
      } else if (namespace !== reservedNamespace) {
        problems.push(locale + "/" + file + ": must contain exactly one top-level key \"" + namespace + "\"");
        continue;
      }
      if (typeof body !== "object" || body === null || Array.isArray(body) || Object.keys(body).length === 0) {
        problems.push(locale + "/" + file + ": namespace must be a nonempty object");
        continue;
      }
      flatten(body, namespace, flat, problems);
    }
    catalogs.set(locale, flat);
  }
  const reference = catalogs.get(referenceLocale) ?? new Map();
  for (const locale of locales) {
    const flat = catalogs.get(locale) ?? new Map();
    for (const key of reference.keys()) {
      if (!flat.has(key)) {
        problems.push(locale + ": missing key " + key);
      } else if (placeholders(flat.get(key)) !== placeholders(reference.get(key))) {
        problems.push(locale + ": placeholder mismatch for " + key);
      }
    }
    for (const [key, message] of flat) {
      if (!reference.has(key)) {
        problems.push(locale + ": extra key " + key);
      }
      const mixing = scriptMixing(locale, key, message);
      if (mixing) {
        problems.push(locale + ": " + mixing);
      }
    }
  }
  const namespaces = new Set((namespaceList ?? []).map((file) => file.slice(0, -".json".length)));
  const sources = sourceFiles(join(webRoot, "src")).map((file) => readFileSync(file, "utf8"));
  const used = referencedKeys(sources, namespaces);
  for (const key of used) {
    if (!reference.has(key)) {
      problems.push("source references missing key " + key);
    }
  }
  for (const key of reference.keys()) {
    if (!used.has(key) && !key.startsWith(reservedNamespace + ".")) {
      problems.push("unused key " + key);
    }
  }
  return { problems, keys: reference.size };
}

function main() {
  const webRoot = resolve(fileURLToPath(new URL("..", import.meta.url)));
  if (!existsSync(join(webRoot, "src", "i18n"))) {
    console.error("i18n check: web/src/i18n is missing");
    return 1;
  }
  const { problems, keys } = checkCatalogs(webRoot);
  if (problems.length > 0) {
    console.error("i18n check failed:");
    for (const problem of problems) {
      console.error("  " + problem);
    }
    return 1;
  }
  console.log("i18n check: " + locales.length + " locales, " + keys + " keys; JSON, parity, placeholders, script and usage passed");
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main();
}
