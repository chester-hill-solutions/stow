import { execFileSync } from "node:child_process";

export function readPreviousBaseline(repoRoot, relativePath) {
  const ref = process.env.STOW_BASELINE_REF?.trim() || "HEAD^";
  let resolved;
  try {
    resolved = execFileSync("git", ["rev-parse", "--verify", ref], {
      cwd: repoRoot,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    throw new Error(`Cannot resolve ratchet base ${ref}; check out full history (fetch-depth: 0) or set STOW_BASELINE_REF`);
  }

  try {
    return JSON.parse(execFileSync("git", ["show", `${resolved}:${relativePath}`], {
      cwd: repoRoot,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }));
  } catch {
    // The first baseline may legitimately have no parent version.
    return null;
  }
}
