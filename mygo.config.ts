import { existsSync, readFileSync } from "node:fs";

const version = readFileSync(new URL("./VERSION", import.meta.url), "utf8").trim();
const clientOnly = process.env.CAGE_CLIENT_BUILD === "1";
const macosTrial = process.env.CAGE_MACOS_TRIAL === "1";
const signingIdentity = process.env.CAGE_SIGNING_IDENTITY || "-";
const notaryProfile = process.env.CAGE_NOTARY_PROFILE || "";
const notaryKeychain = process.env.CAGE_NOTARY_KEYCHAIN || "";
const provisioningProfile = process.env.CAGE_PROVISIONING_PROFILE || "";

const helperEntitlements: Record<string, string> = {};
if (!clientOnly && existsSync("resources/darwin-arm64/cage")) {
  helperEntitlements["cage"] = "third_party/lima/vz.entitlements";
}

const infoPlist: Record<string, unknown> = {
  CageVersion: version,
  CFBundleVersion: process.env.CAGE_BUILD_NUMBER || "1",
  NSAppTransportSecurity: { NSAllowsLocalNetworking: true },
};
if (clientOnly) {
  infoPlist.CageClientOnly = true;
}

const name = clientOnly ? "Airlock Client" : macosTrial ? "Airlock macOS" : "Airlock";
const slug = clientOnly ? "airlock-client" : macosTrial ? "airlock-macos" : "airlock";
const identifier = clientOnly
  ? "io.github.lee-xeo.claude-cage-client"
  : macosTrial
    ? "io.github.lee-xeo.claude-cage.macos-trial"
    : "io.github.lee-xeo.claude-cage";

export default {
  name,
  slug,
  identifier,
  version,
  license: "MIT",
  icon: "resources/icon.png",
  main: "./cmd/cage-desktop",
  out: clientOnly ? "_output/client-dist" : "_output/dist",
  macos: {
    minimumSystemVersion: "14.0",
    signingIdentity,
    dmgTitle: name,
    helperEntitlements,
    infoPlist,
    ...(provisioningProfile ? { provisioningProfile } : {}),
    ...(notaryProfile
      ? {
          notarize: {
            keychainProfile: notaryProfile,
            ...(notaryKeychain ? { keychain: notaryKeychain } : {}),
          },
        }
      : {}),
  },
};
