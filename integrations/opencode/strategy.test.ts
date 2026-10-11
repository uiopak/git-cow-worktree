import { afterEach, expect, test } from "bun:test"
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { isAbsolute, join, relative, resolve, sep } from "node:path"
import { run } from "./command"
import { makeStrategy, markerName, parseWorktrees } from "./strategy"

const git = process.env.COW_TEST_GIT ?? "git"
const cow = process.env.COW_TEST_EXECUTABLE ?? "git-cow-worktree"
const base = resolve(process.env.COW_TEST_ROOT ?? tmpdir())
const directories: string[] = []
const context = () => ({ signal: AbortSignal.timeout(30_000) })
const command = async (cwd: string, args: string[]) => (await run(git, args, cwd, context().signal, git)).trimEnd()
const strategy = () => makeStrategy({ executable: cow, gitExecutable: git })

async function fixture() {
  await mkdir(base, { recursive: true })
  const dir = await mkdtemp(join(base, "cow-plugin-"))
  directories.push(dir)
  const source = join(dir, "source spaces λ")
  await mkdir(source)
  await command(source, ["init", "-b", "main"])
  await command(source, ["config", "user.email", "cow-test@example.invalid"])
  await command(source, ["config", "user.name", "COW integration test"])
  await command(source, ["config", "core.hooksPath", join(source, ".git", "hooks")])
  await writeFile(join(source, ".gitattributes"), "* -text\n")
  await writeFile(join(source, "hello.txt"), "source\n")
  await writeFile(join(source, "binary.bin"), Buffer.alloc(64 * 1024, 137))
  await command(source, ["add", "."])
  await command(source, ["commit", "-m", "initial"])
  const head = await command(source, ["rev-parse", "HEAD"])
  const target = join(dir, "worktree spaces λ")
  return { dir, source, head, target }
}

afterEach(async () => {
  for (const directory of directories.splice(0)) {
    const child = relative(base, directory)
    if (isAbsolute(child) || child.includes(sep) || !child.startsWith("cow-plugin-")) throw new Error("Unsafe test cleanup path")
    await rm(directory, { recursive: true, force: true })
  }
})

test("NUL inventory preserves spaces, Unicode and embedded newlines", () => {
  expect(parseWorktrees("worktree /repo spaces λ\0HEAD aaa\0branch refs/heads/main\0\0worktree /repo\nsecond\0HEAD bbb\0detached\0\0"))
    .toEqual([{ directory: "/repo spaces λ", head: "aaa", branch: "refs/heads/main", bare: false },
      { directory: "/repo\nsecond", head: "bbb", branch: undefined, bare: false }])
})

test("invalid executable and unsupported options fail clearly", () => {
  expect(() => makeStrategy({ executable: "relative/path" })).toThrow("absolute")
  expect(() => makeStrategy({ typo: true })).toThrow("Unknown")
})

test("creates correct checkout, inventories only owned worktrees and removes unchanged branch", async () => {
  const { source, head, target, dir } = await fixture()
  const normal = join(dir, "ordinary")
  await command(source, ["worktree", "add", "--detach", normal, head])
  const backend = strategy()
  const created = await backend.create({ sourceDirectory: source, directory: target }, context())
  expect(await command(created.directory, ["rev-parse", "HEAD"])).toBe(head)
  expect(await command(created.directory, ["status", "--short"])).toBe("")
  expect(await readFile(join(created.directory, "binary.bin"))).toEqual(await readFile(join(source, "binary.bin")))
  expect(await backend.list(source, context())).toHaveLength(2)
  await expect(backend.remove({ directory: normal, force: true }, context())).rejects.toThrow("not owned")
  const branch = await command(target, ["branch", "--show-current"])
  await backend.remove({ directory: target, force: false }, context())
  expect(await command(source, ["branch", "--list", branch])).toBe("")
  expect(await backend.list(source, context())).toHaveLength(1)
})

test("dirty removal requests force confirmation and preserves edits", async () => {
  const { source, target } = await fixture()
  const backend = strategy()
  await backend.create({ sourceDirectory: source, directory: target }, context())
  await writeFile(join(target, "hello.txt"), "edited\n")
  try { await backend.remove({ directory: target, force: false }, context()); throw new Error("Removal unexpectedly succeeded") }
  catch (error) { expect(error).toMatchObject({ forceRequired: true }) }
  expect(await readFile(join(target, "hello.txt"), "utf8")).toBe("edited\n")
  expect(await readFile(join(source, "hello.txt"), "utf8")).toBe("source\n")
  await backend.remove({ directory: target, force: true }, context())
})

test("retains branches with commits when removing their worktrees", async () => {
  const { source, target } = await fixture()
  const backend = strategy()
  await backend.create({ sourceDirectory: source, directory: target }, context())
  const branch = await command(target, ["branch", "--show-current"])
  await writeFile(join(target, "hello.txt"), "committed\n")
  await command(target, ["commit", "-am", "agent work"])
  const commit = await command(target, ["rev-parse", "HEAD"])
  await backend.remove({ directory: target, force: false }, context())
  expect(await command(source, ["rev-parse", branch])).toBe(commit)
})

test("starting ref selects a commit rather than reusing its branch", async () => {
  const { source, target, head } = await fixture()
  await command(source, ["checkout", "-b", "different"])
  await writeFile(join(source, "hello.txt"), "different commit\n")
  await command(source, ["commit", "-am", "different"])
  const backend = strategy()
  await backend.create({ sourceDirectory: source, directory: target, branch: "main" }, context())
  expect(await command(target, ["rev-parse", "HEAD"])).toBe(head)
  expect(await command(target, ["branch", "--show-current"])).toStartWith("opencode/cow-")
  expect(await readFile(join(target, "hello.txt"), "utf8")).toBe("source\n")
  await backend.remove({ directory: target, force: false }, context())
})

test("HEAD resolves in the source linked checkout", async () => {
  const { source, target, dir } = await fixture()
  const linked = join(dir, "linked")
  await command(source, ["worktree", "add", "-b", "linked", linked])
  await writeFile(join(linked, "hello.txt"), "linked commit\n")
  await command(linked, ["commit", "-am", "linked"])
  const head = await command(linked, ["rev-parse", "HEAD"])
  const backend = strategy()
  await backend.create({ sourceDirectory: linked, directory: target }, context())
  expect(await command(target, ["rev-parse", "HEAD"])).toBe(head)
  await backend.remove({ directory: target, force: false }, context())
})

test("existing destinations and invalid refs are never adopted", async () => {
  const { source, target } = await fixture()
  await mkdir(target)
  await writeFile(join(target, "keep.txt"), "keep")
  const backend = strategy()
  await expect(backend.create({ sourceDirectory: source, directory: target }, context())).rejects.toThrow("already exists")
  await expect(backend.create({ sourceDirectory: source, directory: target + "2", branch: "--bad" }, context())).rejects.toThrow("Starting ref")
  expect(await readFile(join(target, "keep.txt"), "utf8")).toBe("keep")
  expect(await command(source, ["branch", "--list", "opencode/cow-*"])).toBe("")
})

test("failing post-checkout hook rolls back the owned checkout and branch", async () => {
  const { source, target } = await fixture()
  await writeFile(join(source, ".git", "hooks", "post-checkout"), "#!/bin/sh\nexit 1\n", { mode: 0o755 })
  await expect(strategy().create({ sourceDirectory: source, directory: target }, context())).rejects.toThrow()
  expect(parseWorktrees(await run(git, ["worktree", "list", "--porcelain", "-z"], source, context().signal, git))).toHaveLength(1)
  expect(await command(source, ["branch", "--list", "opencode/cow-*"])).toBe("")
})

test("pre-aborted creation makes no checkout", async () => {
  const { source, target } = await fixture()
  await expect(strategy().create({ sourceDirectory: source, directory: target }, { signal: AbortSignal.abort(new Error("cancelled")) })).rejects.toThrow("cancelled")
  expect(await command(source, ["branch", "--list", "opencode/cow-*"])).toBe("")
})

test("forged ownership cannot remove an ordinary worktree", async () => {
  const { source, target } = await fixture()
  await command(source, ["worktree", "add", "-b", "ordinary", target])
  const gitDir = await command(target, ["rev-parse", "--absolute-git-dir"])
  await writeFile(join(gitDir, markerName), JSON.stringify({ version: 1, strategy: "git-cow-worktree", directory: target, commonDir: "elsewhere", branch: "refs/heads/opencode/cow-fake", initialHead: "0".repeat(40) }))
  await expect(strategy().remove({ directory: target, force: true }, context())).rejects.toThrow("ownership")
  expect(await readFile(join(target, "hello.txt"), "utf8")).toBe("source\n")
  expect(await strategy().list(source, context())).toHaveLength(1)
})

test("subprocess cancellation stops a spawned child too", async () => {
  const { dir } = await fixture()
  const childPID = join(dir, "child-pid.txt")
  const script = join(dir, "long-command.js")
  await writeFile(script, `const {spawn}=require('node:child_process'); const fs=require('node:fs'); const child=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'inherit'}); fs.writeFileSync(process.argv[2],String(child.pid)); setInterval(()=>{},1000);`)
  const controller = new AbortController()
  const pending = run(process.execPath, [script, childPID], dir, controller.signal, git)
  const outcome = pending.then(() => undefined, (error: Error) => error)
  let pid = 0
  for (let n = 0; n < 100; n++) {
    try { pid = Number(await readFile(childPID, "utf8")); break } catch {}
    await Bun.sleep(20)
  }
  expect(pid).toBeGreaterThan(0)
  controller.abort(new Error("cancelled"))
  expect(await outcome).toMatchObject({ message: "cancelled" })
  for (let n = 0; n < 100; n++) {
    try { process.kill(pid, 0) } catch { break }
    await Bun.sleep(20)
  }
  expect(() => process.kill(pid, 0)).toThrow()
})
