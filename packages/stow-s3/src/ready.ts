// Client half of the session readiness protocol.
//
// The server writes one JSON object to an inherited file descriptor. Parsing is
// strict on purpose: an unknown protocol version or a missing field is a
// specific error, not a partially populated session that fails later in a
// confusing place.
export const READY_PROTOCOL_VERSION = 1;

export interface StowReadyCapabilities {
  persistent: boolean;
  multipart: boolean;
  upstream: boolean;
  conditionalWrites: boolean;
  presignedUrls: boolean;
  maxBytes: number;
  maxObjects: number;
  maxRequestBytes: number;
}

export interface StowReady {
  protocolVersion: number;
  binaryVersion: string;
  endpoint: string;
  region: string;
  accessKeyId: string;
  secretAccessKey: string;
  mode: string;
  backend: string;
  capabilities: StowReadyCapabilities;
}

export class StowProtocolError extends Error {
  readonly code: "protocol_mismatch" | "internal";

  constructor(code: "protocol_mismatch" | "internal", message: string) {
    super(message);
    this.name = "StowProtocolError";
    this.code = code;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function requireString(source: Record<string, unknown>, field: string): string {
  const value = source[field];
  if (typeof value !== "string" || value.length === 0) {
    throw new StowProtocolError("protocol_mismatch", `ready message is missing ${field}`);
  }
  return value;
}

function requireBoolean(source: Record<string, unknown>, field: string): boolean {
  const value = source[field];
  if (typeof value !== "boolean") {
    throw new StowProtocolError("protocol_mismatch", `ready message is missing ${field}`);
  }
  return value;
}

function requireNumber(source: Record<string, unknown>, field: string): number {
  const value = source[field];
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new StowProtocolError("protocol_mismatch", `ready message is missing ${field}`);
  }
  return value;
}

/**
 * Parse the single readiness object. Throws StowProtocolError on an unknown
 * protocol version, on a non-object payload, or on any missing field.
 */
export function parseReadyMessage(line: string): StowReady {
  let decoded: unknown;
  try {
    decoded = JSON.parse(line);
  } catch (error) {
    throw new StowProtocolError(
      "protocol_mismatch",
      `ready message is not valid JSON: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
  if (!isRecord(decoded)) {
    throw new StowProtocolError("protocol_mismatch", "ready message is not a JSON object");
  }
  const version = requireNumber(decoded, "protocolVersion");
  if (version !== READY_PROTOCOL_VERSION) {
    throw new StowProtocolError(
      "protocol_mismatch",
      `unsupported ready protocol version ${version}; this client speaks ${READY_PROTOCOL_VERSION}`,
    );
  }
  const capabilities = decoded.capabilities;
  if (!isRecord(capabilities)) {
    throw new StowProtocolError("protocol_mismatch", "ready message is missing capabilities");
  }
  return {
    protocolVersion: version,
    binaryVersion: requireString(decoded, "binaryVersion"),
    endpoint: requireString(decoded, "endpoint"),
    region: requireString(decoded, "region"),
    accessKeyId: requireString(decoded, "accessKeyId"),
    secretAccessKey: requireString(decoded, "secretAccessKey"),
    mode: requireString(decoded, "mode"),
    backend: requireString(decoded, "backend"),
    capabilities: {
      persistent: requireBoolean(capabilities, "persistent"),
      multipart: requireBoolean(capabilities, "multipart"),
      upstream: requireBoolean(capabilities, "upstream"),
      conditionalWrites: requireBoolean(capabilities, "conditionalWrites"),
      presignedUrls: requireBoolean(capabilities, "presignedUrls"),
      maxBytes: requireNumber(capabilities, "maxBytes"),
      maxObjects: requireNumber(capabilities, "maxObjects"),
      maxRequestBytes: requireNumber(capabilities, "maxRequestBytes"),
    },
  };
}
