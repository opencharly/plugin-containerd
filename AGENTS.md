# AGENTS.md — plugin-containerd

Standalone out-of-tree plugin repo serving the `engine:containerd` engine backend
(`engine:containerd` + `verb:containerd` + `command:containerd`). `containerd` is
the ENGINE WORD; `nerdctl` is only the CLI binary it is driven through (the
CUE-owned `#EngineBinaries` map), never an engine word. The plugin is a Go
module at `candy/plugin-containerd/` (module path
`github.com/opencharly/plugin-containerd/candy/plugin-containerd`); the root
`charly.yml` declares `discover: box` + `discover: candy` so the repo is a
project and its candy is scanned.

Canonical files:

- `candy/plugin-containerd/charly.yml` — the `plugin-containerd:` candy entity
  (`plugin:` block, `plan:` checks) and the embedded `plugin-containerd-skill:`
  skill entity.
- `candy/plugin-containerd/` — the Go source: `plugin.go` (provider + meta + the
  containerd start-plan policy), `verb.go` (the READ-ONLY `containerd:` check
  verbs), `schema/plugin.cue`, `params/` (generated from the schema by
  `params/gen.sh`; never hand-edit `cue_types_gen.go`), `cmd/serve/main.go`.
- `.agents/skills/plugin-containerd/SKILL.md` — the skill body, the one canonical
  base the corpus projects `/charly-internals:plugin-containerd` from. The candy's
  `plugin-containerd-skill:` entity is the owner registration paired with that body.
- `README.md` — user overview only; never agent guidance.

The repo carries only the `tag-on-merge` caller (`.github/workflows/`); the
org-wide required workflow is what gates the merge.

## Load these skills first (R0)

- `/charly-internals:plugin-containerd` — the `engine:containerd` provider reference
  (projected from this repo's `.agents/skills/plugin-containerd/SKILL.md`). Load
  before changing an engine op.
- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:`
  block, the unified Provider model, the per-plugin CUE-schema contract,
  placement.
- `/charly-distros:layer-nerdctl` — the packages + rootless
  containerd/buildkit/CNI stack this plugin composes with.
- `/charly-internals:git-workflow` — before any git/PR action.

## Build / validate / test

- `go build ./...` in `candy/plugin-containerd/` — compile the plugin module.
- `go test ./...` in `candy/plugin-containerd/` — the plugin's Go tests, including
  the out-of-process go-plugin round trip (skipped with `-short`). The live
  containerd check-verb arm is opt-in: `CHARLY_CONTAINERD_LIVE=1 go test ./...
  -run TestVerbLiveLocal` (and `CHARLY_CONTAINERD_LIVE_IMAGE=<ref>` for the
  `image`/`krun_guest` methods); without it the arm skips visibly.
- `go generate ./params` in `candy/plugin-containerd/` — regenerate
  `params/cue_types_gen.go` from `schema/plugin.cue`; a second run is a no-op.
  This is the one DEV-TIME step that needs the `cue` binary (invoked by
  `params/gen.sh`); build/test/`box validate` need only `go` and `charly`.
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate.

## Modify this repo

- Edit the `plugin-containerd:` candy entity, the Go source, and `schema/plugin.cue`
  **together** — the schema is the served declaration surface.
- Keep the delegation to `container.InvokeEngineOp("containerd", …)` in the spec
  module: the engine class has ONE implementation served from either placement.
  Do not fork the engine body here.
- The PROVIDER-SPECIFIC policy belongs in the provider: `start_plan` always names
  a runtime handler (nerdctl's unqualified default is runc, the CRI default
  crun), and a rootful plan is prefixed with the capability's
  `privilege_prefix` — never a hard-coded escalation, never silently rootless.
- Keep `nerdctl` out of the engine vocabulary. It is the CLI binary named by
  `spec.EngineBinaries["containerd"]`; a second engine spelling is a cutover
  regression.
- Keep `.agents/skills/plugin-containerd/SKILL.md` in step with any provider-surface
  change: it is the skill BODY, the one canonical base the corpus reader resolves.
  The candy's `plugin-containerd-skill:` entity is the owner registration the reader
  pairs with that body (name/family/owner only — it carries no body); update its
  metadata only when the name/family/owner changes.

## Landing

Load `/charly-internals:git-workflow` before any git/PR action; it owns the
landing mechanics. The authoritative rulebook is the umbrella `AGENTS.md` in
`opencharly/opencharly` and `charly/AGENTS.md` in the charly repo.
