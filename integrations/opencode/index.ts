import { Plugin } from "@opencode/plugin"
import { makeStrategy } from "./strategy"

export default Plugin.define({
  id: "git-cow-worktree",
  async setup(ctx) {
    if (!ctx.worktree?.transform) {
      throw new Error("git-cow-worktree requires OpenCode v2 with the worktree strategy API; tested with 2.0.26")
    }
    const registration = await ctx.worktree.transform((editor) => editor.add(makeStrategy(ctx.options)))
    return () => registration.dispose()
  },
})
