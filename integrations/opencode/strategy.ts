import { randomUUID } from "node:crypto"
import { lstat, readFile, realpath, writeFile } from "node:fs/promises"
import { basename, dirname, isAbsolute, join, resolve } from "node:path"
import { Worktree } from "@opencode/plugin"
import type { WorktreeDefinition } from "@opencode/plugin/promise/worktree"
import { CommandError, run } from "./command"

export const markerName = "git-cow-opencode.json"
const strategyID = "git-cow-worktree"
interface Marker { version: number; strategy: string; directory: string; commonDir: string; branch: string; initialHead: string }
interface Checkout { directory: string; branch?: string; head?: string; bare: boolean }

export function parseWorktrees(data: string): Checkout[] {
  return data.split("\0\0").filter(Boolean).map((record) => {
    const fields = record.split("\0")
    const directory = fields.find((field) => field.startsWith("worktree "))?.slice(9)
    if (!directory) throw new Error("Invalid Git worktree inventory")
    return { directory, bare: fields.includes("bare"),
      branch: fields.find((field) => field.startsWith("branch "))?.slice(7),
      head: fields.find((field) => field.startsWith("HEAD "))?.slice(5) }
  })
}

function samePath(a: string, b: string): boolean {
  const normalize = (value: string) => process.platform === "win32" ? resolve(value).toLowerCase() : resolve(value)
  return normalize(a) === normalize(b)
}

async function noSymlinks(directory: string): Promise<void> {
  for (let current = resolve(directory); ; current = dirname(current)) {
    try {
      if ((await lstat(current)).isSymbolicLink()) throw new Error(`Refusing symlinked worktree path: ${current}`)
    } catch (error) { if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error }
    if (dirname(current) === current) return
  }
}

// Git expands Windows short names in its worktree inventory. Resolve the
// existing parent before creation, including when Git must create directories.
async function canonicalDestination(directory: string): Promise<string> {
  const missing: string[] = []
  for (let current = directory; ; current = dirname(current)) {
    try { return join(await realpath(current), ...missing.reverse()) }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT" || dirname(current) === current) throw error
    }
    missing.push(basename(current))
  }
}

function executable(value: unknown, fallback: string): string {
  const name = value ?? fallback
  if (typeof name !== "string" || !name.trim() || name.includes("\0") || (!isAbsolute(name) && /[/\\]/.test(name))) {
    throw new Error("Executables must be command names or absolute paths")
  }
  return name
}

export function makeStrategy(options: Record<string, unknown> = {}): WorktreeDefinition {
  for (const key of Object.keys(options)) if (key !== "executable" && key !== "gitExecutable") throw new Error(`Unknown git-cow-worktree option: ${key}`)
  const cow = executable(options.executable, "git-cow-worktree")
  const git = executable(options.gitExecutable, "git")
  const query = (cwd: string, args: string[], signal: AbortSignal) => run(git, args, cwd, signal, git)
  const text = async (cwd: string, args: string[], signal: AbortSignal) => (await query(cwd, args, signal)).replace(/\r?\n$/, "")
  const inventory = async (cwd: string, signal: AbortSignal) => parseWorktrees(await query(cwd, ["worktree", "list", "--porcelain", "-z"], signal))
  const commonDir = async (cwd: string, signal: AbortSignal) => realpath(await text(cwd, ["rev-parse", "--path-format=absolute", "--git-common-dir"], signal))
  const root = (checkouts: Checkout[]) => {
    if (!checkouts[0] || checkouts[0].bare) throw new Error("OpenCode COW requires a repository with a main checkout")
    return checkouts[0].directory
  }

  async function owned(checkout: Checkout, common: string, signal: AbortSignal): Promise<Marker | undefined> {
    if (checkout.bare) return
    await noSymlinks(checkout.directory)
    const gitDir = await text(checkout.directory, ["rev-parse", "--absolute-git-dir"], signal)
    let marker: Marker
    try { marker = JSON.parse(await readFile(join(gitDir, markerName), "utf8")) }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") return
      if (error instanceof SyntaxError) throw new Error(`Invalid OpenCode COW ownership record: ${checkout.directory}`)
      throw error
    }
    if (!marker || marker.version !== 1 || marker.strategy !== strategyID ||
      typeof marker.directory !== "string" || typeof marker.commonDir !== "string" ||
      typeof marker.branch !== "string" || !marker.branch.startsWith("refs/heads/opencode/cow-") ||
      !/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(marker.initialHead) ||
      !samePath(marker.directory, checkout.directory) || !samePath(marker.commonDir, common) || marker.branch !== checkout.branch ||
      !samePath(await commonDir(checkout.directory, signal), common)) {
      throw new Error(`Invalid OpenCode COW ownership record: ${checkout.directory}`)
    }
    return marker
  }

  async function deleteUnchangedBranch(cwd: string, branch: string, initialHead: string, signal: AbortSignal) {
    if ((await inventory(cwd, signal)).some((checkout) => checkout.branch === branch)) return
    let current: string
    try { current = await text(cwd, ["rev-parse", "--verify", "--end-of-options", branch], signal) }
    catch (error) { if (error instanceof CommandError) return; throw error }
    if (current !== initialHead) return
    // Compare-and-delete preserves a branch that acquired commits meanwhile.
    try { await query(cwd, ["update-ref", "-d", branch, initialHead], signal) }
    catch (error) { if (!(error instanceof CommandError)) throw error }
  }

  async function rollback(cwd: string, directory: string, branch: string, head: string) {
    const signal = AbortSignal.timeout(30_000)
    const checkout = (await inventory(cwd, signal)).find((entry) => samePath(entry.directory, directory))
    if (checkout) {
      if (checkout.branch !== branch || checkout.head !== head) throw new Error(`Failed creation left changed worktree at ${directory}; retained for inspection`)
      // Dirty files from a failing checkout hook are retained too.
      await query(cwd, ["worktree", "remove", "--", directory], signal)
    }
    await deleteUnchangedBranch(cwd, branch, head, signal)
  }

  return {
    id: strategyID,
    async create(input, { signal }) {
      signal.throwIfAborted()
      const source = await realpath(input.sourceDirectory)
      const requestedDirectory = resolve(input.directory)
      await noSymlinks(requestedDirectory)
      const directory = await canonicalDestination(requestedDirectory)
      try { await lstat(directory); throw new Error(`Worktree destination already exists: ${directory}`) }
      catch (error) { if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error }
      root(await inventory(source, signal))
      const common = await commonDir(source, signal)
      const ref = input.branch ?? "HEAD"
      if (typeof ref !== "string" || !ref || ref.startsWith("-") || ref.includes("\0")) throw new Error("Starting ref must be non-empty and must not start with '-'")
      const head = await text(source, ["rev-parse", "--verify", "--end-of-options", `${ref}^{commit}`], signal)
      const branch = `refs/heads/opencode/cow-${randomUUID()}`
      try {
        await run(cow, ["add", "-b", branch.slice(11), "--", directory, head], source, signal, git)
        signal.throwIfAborted()
        const checkout = (await inventory(source, signal)).find((entry) => samePath(entry.directory, directory))
        if (!checkout || checkout.branch !== branch || checkout.head !== head || !samePath(await commonDir(directory, signal), common)) {
          throw new Error("COW executable returned an unexpected Git worktree")
        }
        const actual = await realpath(directory)
        const gitDir = await text(actual, ["rev-parse", "--absolute-git-dir"], signal)
        const marker: Marker = { version: 1, strategy: strategyID, directory: actual, commonDir: common, branch, initialHead: head }
        await writeFile(join(gitDir, markerName), JSON.stringify(marker) + "\n", { flag: "wx" })
        signal.throwIfAborted()
        return { directory: actual }
      } catch (error) {
        try { await rollback(source, directory, branch, head) }
        catch (cleanupError) { throw new AggregateError([error, cleanupError], `COW creation failed; inspect ${directory}`) }
        throw error
      }
    },
    async list(sourceDirectory, { signal }) {
      const source = await realpath(sourceDirectory)
      const checkouts = await inventory(source, signal)
      const common = await commonDir(source, signal)
      const entries: { directory: string; type: "root" | "worktree" }[] = [{ directory: root(checkouts), type: "root" }]
      for (const checkout of checkouts.slice(1)) {
        try { if (await owned(checkout, common, signal)) entries.push({ directory: checkout.directory, type: "worktree" }) }
        catch (error) {
          signal.throwIfAborted()
          // Missing, inaccessible or malformed directories are not adopted.
          if (error instanceof CommandError || (error as NodeJS.ErrnoException).code === "ENOENT" ||
            (error instanceof Error && /ownership record|symlinked/.test(error.message))) continue
          throw error
        }
      }
      return entries
    },
    async remove(input, { signal }) {
      signal.throwIfAborted()
      await noSymlinks(input.directory)
      const directory = await realpath(input.directory)
      const checkouts = await inventory(directory, signal)
      const cwd = root(checkouts)
      const checkout = checkouts.slice(1).find((entry) => samePath(entry.directory, directory))
      const marker = checkout && await owned(checkout, await commonDir(cwd, signal), signal)
      if (!marker) throw new Error(`Refusing to remove a worktree not owned by OpenCode COW: ${directory}`)
      if (!input.force && (await query(directory, ["status", "--porcelain=v1", "-z", "--untracked-files=all"], signal))) {
        throw new Worktree.OperationError({ message: `Worktree has uncommitted changes: ${directory}`, forceRequired: true })
      }
      const args = ["worktree", "remove"]
      if (input.force) args.push("--force")
      await query(cwd, [...args, "--", directory], signal)
      await deleteUnchangedBranch(cwd, marker.branch, marker.initialHead, signal)
    },
  }
}
