#!/usr/bin/env node
"use strict";

const https = require("https");
const http = require("http");
const fs = require("fs");
const path = require("path");
const { execSync } = require("child_process");
const PLATFORM_MAP = {
  linux: "linux",
  darwin: "darwin",
  win32: "windows",
};

const ARCH_MAP = {
  x64: "amd64",
  arm64: "arm64",
};

function getPackageVersion() {
  const pkg = JSON.parse(
    fs.readFileSync(path.join(__dirname, "package.json"), "utf8")
  );
  return pkg.version;
}

function getDownloadUrl(version, os, arch) {
  const ext = os === "windows" ? "zip" : "tar.gz";
  return `https://github.com/policylayer/intercept/releases/download/v${version}/intercept-${os}-${arch}.${ext}`;
}

function fetch(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    if (redirects > 10) {
      return reject(new Error("Too many redirects"));
    }
    const client = url.startsWith("https") ? https : http;
    client
      .get(url, { headers: { "User-Agent": "policylayer-intercept-npm" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          return fetch(res.headers.location, redirects + 1).then(resolve, reject);
        }
        if (res.statusCode !== 200) {
          return reject(new Error(`Download failed: HTTP ${res.statusCode} from ${url}`));
        }
        const chunks = [];
        res.on("data", (chunk) => chunks.push(chunk));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

function extractTarGz(buffer, destDir) {
  const tmpFile = path.join(destDir, "_tmp.tar.gz");
  fs.writeFileSync(tmpFile, buffer);
  try {
    execSync(`tar -xzf "${tmpFile}" -C "${destDir}"`, { stdio: "ignore" });
  } finally {
    fs.unlinkSync(tmpFile);
  }
}

function extractZip(buffer, destDir) {
  const tmpFile = path.join(destDir, "_tmp.zip");
  fs.writeFileSync(tmpFile, buffer);
  try {
    // PowerShell is available on all supported Windows versions
    execSync(
      `powershell -NoProfile -Command "Expand-Archive -Force '${tmpFile}' '${destDir}'"`,
      { stdio: "ignore" }
    );
  } finally {
    fs.unlinkSync(tmpFile);
  }
}

async function main() {
  const platform = PLATFORM_MAP[process.platform];
  const arch = ARCH_MAP[process.arch];

  if (!platform || !arch) {
    console.error(
      `Unsupported platform: ${process.platform}-${process.arch}`
    );
    process.exit(1);
  }

  const version = getPackageVersion();
  if (version === "0.0.0") {
    console.error(
      "Package version is 0.0.0. Binary download is only available for published releases."
    );
    process.exit(1);
  }

  const url = getDownloadUrl(version, platform, arch);
  const binDir = path.join(__dirname, "bin");
  const binaryName = platform === "windows" ? "intercept.exe" : "intercept";
  const binaryPath = path.join(binDir, binaryName);

  console.log(`Downloading intercept v${version} for ${platform}/${arch}...`);

  try {
    const buffer = await fetch(url);

    fs.mkdirSync(binDir, { recursive: true });

    if (platform === "windows") {
      extractZip(buffer, binDir);
    } else {
      extractTarGz(buffer, binDir);
    }

    fs.chmodSync(binaryPath, 0o755);
    console.log(`Installed intercept to ${binaryPath}`);
  } catch (err) {
    console.error(`Failed to install intercept: ${err.message}`);
    process.exit(1);
  }
}

main();
