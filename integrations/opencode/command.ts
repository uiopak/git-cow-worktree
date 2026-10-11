import { spawn } from "node:child_process"
import { dirname, isAbsolute, join } from "node:path"

export class CommandError extends Error {
  constructor(readonly code: number | null, readonly stderr: string, command: string) {
    super(`${command}: ${stderr.trim() || `exited with code ${code}`}`)
  }
}

// Commands use argument vectors, not a shell. On cancellation, stop the process
// tree before resolving so Git children cannot continue a checkout afterward.
export function run(executable: string, args: string[], cwd: string, signal: AbortSignal, git: string): Promise<string> {
  signal.throwIfAborted()
  return new Promise((resolve, reject) => {
    const env: NodeJS.ProcessEnv = { ...process.env, GIT_COW_SHIM_BYPASS: "1" }
    if (isAbsolute(git)) env.PATH = dirname(git) + (process.platform === "win32" ? ";" : ":") + (env.PATH ?? "")
    const child = spawn(executable, args, {
      cwd, env, shell: false, windowsHide: true,
      detached: process.platform !== "win32",
      stdio: ["ignore", "pipe", "pipe"],
    })
    let stdout = "", stderr = ""
    let failure: Error | undefined
    let stopping: Promise<void> | undefined
    const stop = () => {
      if (stopping || !child.pid) return
      const pid = child.pid
      stopping = new Promise<void>((done) => {
        if (process.platform === "win32") {
          const killer = spawn(join(process.env.SystemRoot ?? "C:\\Windows", "System32", "taskkill.exe"),
            ["/PID", String(pid), "/T", "/F"], { windowsHide: true, stdio: "ignore" })
          killer.once("error", () => { child.kill(); done() })
          killer.once("close", () => { if (child.exitCode === null) child.kill(); done() })
        } else {
          try { process.kill(-pid, "SIGKILL") } catch { child.kill("SIGKILL") }
          done()
        }
      })
    }
    const abort = () => { stop() }
    child.stdout.setEncoding("utf8")
    child.stderr.setEncoding("utf8")
    child.stdout.on("data", (chunk: string) => {
      if (stdout.length + chunk.length > 8 * 1024 * 1024) { failure ??= new Error("Command stdout exceeded 8 MiB"); stop() }
      else stdout += chunk
    })
    child.stderr.on("data", (chunk: string) => {
      if (stderr.length + chunk.length > 8 * 1024 * 1024) { failure ??= new Error("Command stderr exceeded 8 MiB"); stop() }
      else stderr += chunk
    })
    child.once("error", (error) => { failure ??= error })
    child.once("close", async (code) => {
      signal.removeEventListener("abort", abort)
      await stopping
      if (signal.aborted) reject(signal.reason)
      else if (failure) reject(failure)
      else if (code !== 0) reject(new CommandError(code, stderr, executable))
      else resolve(stdout)
    })
    signal.addEventListener("abort", abort, { once: true })
    if (signal.aborted) abort()
  })
}
