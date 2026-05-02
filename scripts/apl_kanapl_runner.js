#!/usr/bin/env node
"use strict";

const fs = require("fs");

function loadKanapl() {
  try {
    return require("kanapl");
  } catch (error) {
    if (error && error.code !== "MODULE_NOT_FOUND") {
      throw error;
    }
    return require("/usr/local/lib/node_modules/kanapl");
  }
}

function usage() {
  process.stderr.write("usage: apl [--script|-s] [-f file]\n");
}

function formatValue(value) {
  if (Array.isArray(value)) {
    if (value.every((item) => typeof item === "string" && item.length === 1)) {
      return value.join("");
    }
    return value.map(formatValue).join(" ");
  }
  if (value === undefined || value === null) {
    return "";
  }
  return String(value);
}

const args = process.argv.slice(2);
let file = "";

for (let index = 0; index < args.length; index += 1) {
  const arg = args[index];
  if (arg === "--version" || arg === "-v") {
    process.stdout.write("KANAPL 0.0.0\n");
    process.exit(0);
  }
  if (arg === "--help" || arg === "-h") {
    usage();
    process.exit(0);
  }
  if (arg === "--script" || arg === "-s" || arg === "--silent" || arg === "--noCIN" || arg === "--noCONT" || arg === "--noColor") {
    continue;
  }
  if (arg === "-f") {
    index += 1;
    file = args[index] || "";
    continue;
  }
  if (arg.startsWith("-")) {
    process.stderr.write(`unsupported apl option: ${arg}\n`);
    process.exit(2);
  }
  file = arg;
}

const source = file ? fs.readFileSync(file, "utf8") : fs.readFileSync(0, "utf8");
const apl = loadKanapl()();

for (const rawLine of source.split(/\r?\n/)) {
  const line = rawLine.trim();
  if (!line || line.startsWith("#")) {
    continue;
  }
  if (line.startsWith("\u2395\u2190")) {
    process.stdout.write(`${formatValue(apl.eval(line.slice(2).trim()))}\n`);
  } else {
    apl.eval(line);
  }
}
