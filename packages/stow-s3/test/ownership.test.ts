import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { after, describe, it } from "node:test";

import {
  resetOwnedData,
  STOW_OWNER_MARKER,
  StowOwnershipError,
} from "../src/ownership.ts";

async function unownedDir(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "stow-unowned-"));
  await writeFile(join(dir, "important.txt"), "not stow data\n", "utf8");
  return dir;
}

async function ownedDir(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "stow-owned-"));
  await writeFile(
    join(dir, STOW_OWNER_MARKER),
    JSON.stringify({ owner: "stow", version: 1 }),
    "utf8",
  );
  return dir;
}

async function gone(path: string): Promise<boolean> {
  try {
    // Probe the directory itself. Reading a file inside it would report ENOENT
    // for a surviving directory that simply has no such file, which reads as a
    // successful delete when nothing was deleted.
    const info = await stat(path);
    return !info.isDirectory();
  } catch {
    return true;
  }
}

describe("resetOwnedData", () => {
  it("deletes a directory stow created", async () => {
    const dir = await ownedDir();
    await writeFile(join(dir, "buckets"), "", "utf8");

    await resetOwnedData(dir);

    assert.ok(await gone(dir), "owned directory should have been removed");
  });

  it("refuses a directory stow did not create, and leaves it intact", async () => {
    const dir = await unownedDir();

    await assert.rejects(() => resetOwnedData(dir), StowOwnershipError);
    assert.ok(!(await gone(dir)), "unowned directory must survive the refusal");
    assert.match(
      (await readFile(join(dir, "important.txt"), "utf8")),
      /not stow data/,
      "the caller's own file must be untouched",
    );
  });

  it("treats an absent directory as already reset", async () => {
    // Not an error: a caller resetting before the first run is normal, and
    // failing there would be noise rather than safety.
    await resetOwnedData(join(tmpdir(), "stow-never-existed-abc123"));
  });

  it("refuses the home directory", async () => {
    await assert.rejects(() => resetOwnedData(homedir()), (error: unknown) => {
      assert.ok(error instanceof StowOwnershipError);
      assert.match((error as Error).message, /home directory/);
      return true;
    });
  });

  it("refuses the filesystem root", async () => {
    await assert.rejects(() => resetOwnedData("/"), StowOwnershipError);
  });

  it("refuses the working directory", async () => {
    await assert.rejects(() => resetOwnedData(process.cwd()), (error: unknown) => {
      assert.ok(error instanceof StowOwnershipError);
      assert.match((error as Error).message, /working directory/);
      return true;
    });
  });

  it("refuses a relative path that climbs to a protected directory", async () => {
    // The bypass this exists to close. From /home/dev/project, ".." is the home
    // directory and "../.." is the filesystem root, but neither string is
    // literally either path, so an equality check would wave them through.
    const escapes = ["..", join("..", ".."), join("..", "..", "..")];
    for (const escape of escapes) {
      await assert.rejects(
        () => resetOwnedData(escape),
        StowOwnershipError,
        `${escape} should be refused`,
      );
    }
  });

  it("still allows a subdirectory of the working directory", async () => {
    // The protected-path rule must not be so blunt that the documented
    // dataDir: ".stow" default stops working.
    const dir = resolve(join(process.cwd(), ".stow-ownership-test"));
    await mkdir(dir, { recursive: true });
    await writeFile(join(dir, STOW_OWNER_MARKER), '{"owner":"stow"}', "utf8");
    after(() => resetOwnedData(dir));

    await resetOwnedData(dir);

    assert.ok(await gone(dir), "a stow subdirectory of the working directory should be resettable");
  });
});
