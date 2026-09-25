import type { UpstreamConfig } from "./types.js";

function envFirst(keys: string[]): string | undefined {
  for (const key of keys) {
    const value = process.env[key]?.trim();
    if (value) {
      return value;
    }
  }
  return undefined;
}

/**
 * Resolve upstream credentials from environment variables.
 * Precedence: STOW_* > S3_* > AWS_*.
 */
export function upstreamFromEnv(): UpstreamConfig | null {
  const endpoint = envFirst([
    "STOW_ENDPOINT",
    "S3_ENDPOINT",
    "AWS_ENDPOINT_URL_S3",
    "AWS_ENDPOINT_URL",
    "AWS_ENDPOINT",
  ]);
  const accessKey = envFirst([
    "STOW_ACCESS_KEY_ID",
    "S3_ACCESS_KEY_ID",
    "AWS_ACCESS_KEY_ID",
  ]);
  const secretKey = envFirst([
    "STOW_SECRET_ACCESS_KEY",
    "S3_SECRET_ACCESS_KEY",
    "AWS_SECRET_ACCESS_KEY",
  ]);
  const sessionToken = envFirst([
    "STOW_SESSION_TOKEN",
    "S3_SESSION_TOKEN",
    "AWS_SESSION_TOKEN",
  ]);

  if (!endpoint || !accessKey || !secretKey) {
    return null;
  }

  return {
    endpoint,
    accessKey,
    secretKey,
    sessionToken,
    region: envFirst([
      "STOW_REGION",
      "S3_REGION",
      "AWS_REGION",
      "AWS_DEFAULT_REGION",
    ]),
    bucket: envFirst(["STOW_BUCKET", "S3_BUCKET"]),
  };
}
