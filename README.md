# plugin-containerd

The out-of-tree charly ENGINE plugin serving `engine:containerd` — the containerd
engine backend for OpenCharly pods, plus the `verb:containerd` /
`command:containerd` convenience words.

`containerd` is the ENGINE WORD. `nerdctl` is only the CLI BINARY that drives it:
the CUE-owned `#EngineBinaries` map (`containerd → nerdctl`) is the one home for
that mapping, and the retired engine spelling is gone (R5, no alias).

The plugin is a standalone Go module: charly's loader host-builds `./cmd/serve`
and connects it **out-of-process** over go-plugin gRPC via the plugin SDK. It
answers every engine op by delegating to the pure body
`container.InvokeEngineOp("containerd", …)` in the spec module — the SAME body the
compiled-in `engine:podman` / `engine:docker` providers serve — so the engine
class has one implementation and two placements.

## What it provides

| Capability | Surface |
|---|---|
| `engine:containerd` | the engine provider-class op envelope: `describe` / `binary` / `gpu_args` / `start_plan` / `unit_emit` / `network_ensure` |
| `verb:containerd` | the read-only `containerd:` check verb: `info` / `runtimes` / `cdi_devices` / `criu_preflight` / `krun_guest` / `image` |
| `command:containerd` | the `charly containerd` convenience command |

The start plan always names an OCI runtime handler (nerdctl's unqualified default
is `runc`, the CRI default `crun`), and a rootful plan is prefixed with the
capability's `privilege_prefix` (e.g. `sudo -n`) instead of being treated as
rootless. The capability defaults for `address` / `namespace` are never
re-spelled; an override rides `extra_args`.

`unit_emit` is a pure renderer and its envelope carries no scope field: a
rootful unit is a user unit whose `ExecStart` is the (prefixed,
`--runtime`-bearing) start-plan argv the caller supplies. This provider does not
re-apply the policy at the unit boundary.

Composes with the `layer-nerdctl` candy (the packages + rootless
containerd/buildkit/CNI stack) to deploy a pod with `engine: containerd`.

## How to use it

Compose the plugin candy alongside the layer candy in a box's `candy:` list:

```yaml
- '@github.com/opencharly/plugin-containerd/candy/plugin-containerd:<tag>'
- '@github.com/opencharly/layer-nerdctl/candy/layer-nerdctl:<tag>'
```

Then select the engine on the deploy:

```yaml
engine: containerd
```

## Layout

- `candy/plugin-containerd/` — the plugin module: `plugin.go` (provider + meta +
  the containerd start-plan policy), `verb.go` (the read-only `containerd:` check
  verbs), `schema/plugin.cue`, `params/` (generated), and `cmd/serve/main.go`.
- `.agents/skills/plugin-containerd/SKILL.md` — the skill body, the one canonical
  base the corpus projects `/charly-internals:plugin-containerd` from. The candy's
  `plugin-containerd-skill:` entity is the owner registration paired with that body
  (name/family/owner only); it carries no body.
- `charly.yml` — the root project manifest (`discover: box` + `discover: candy`).

## Related

- Owning skill: `/charly-internals:plugin-containerd` — the engine:containerd
  provider reference (projected from this repo's skill body).
- `/charly-internals:plugin` — the plugin/provider model and placement.
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
