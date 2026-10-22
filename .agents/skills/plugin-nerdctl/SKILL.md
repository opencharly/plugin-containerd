---
name: plugin-nerdctl
description: The out-of-tree charly ENGINE plugin serving engine:containerd (verb:containerd
  / command:containerd) — every engine op delegates to container.InvokeEngineOp. `containerd`
  is the engine word; `nerdctl` is only the CLI binary it is driven through. Use when working
  with the engine:containerd provider, the plugin-nerdctl candy, or the engine provider-class
  placement model.
---

# plugin-nerdctl

The out-of-tree charly ENGINE plugin serving `engine:containerd` (plus the
`verb:containerd` / `command:containerd` convenience words).

`containerd` is the ENGINE WORD. `nerdctl` is only the CLI BINARY that drives it —
the CUE-owned `#EngineBinaries` map (`containerd → nerdctl`) is the one home for
that mapping, and the retired engine spelling is gone (R5, no alias).

## What it is

A standalone Go module (`go.mod` + `cmd/serve`) served over go-plugin gRPC via
the charly plugin SDK. The charly loader host-builds `./cmd/serve` and connects
it OUT-OF-PROCESS; the SAME `NewProvider()`/`NewMeta()` compile INTO charly
in-process when listed in `compiled_plugins` — placement is invisible above the
registry.

## The one body, two placements

Every engine op (`describe` / `binary` / `gpu_args` / `start_plan` /
`unit_emit` / `network_ensure`) is answered by delegating to the pure body
`container.InvokeEngineOp("containerd", …)` in the spec module — the SAME body
the compiled-in `engine:podman` / `engine:docker` providers serve. So the engine
class has one implementation and two placements.

## The containerd start-plan policy

The one provider-specific policy is applied to `start_plan`:

- **A runtime handler is ALWAYS named.** nerdctl's unqualified default handler is
  `runc` while the CRI default is `crun`, so an unqualified plan would silently
  select a different handler; the plugin fills an empty `runtime` with `crun`
  (validated against the capability's `runtime_handlers`) so `--runtime` is
  always emitted.
- **Rootful is not silently rootless.** A rootful plan gets the capability's
  `privilege_prefix` (e.g. `sudo -n`) as an argv prefix, taken from data. The
  `unit_emit` envelope carries no scope field, so the rendered unit stays a user
  unit whose `ExecStart` carries that prefixed argv.
- **Defaults are not re-spelled.** No `--address` / `--namespace` is emitted for
  the capability defaults; a caller's override rides `extra_args`.

The capability row itself (`supports_rootless` / `supports_rootful`, the
mode-split workload users, `address`, `namespace`, `privilege_prefix`,
`substrates`, `runtime_handlers`) is served by the spec module's engine body —
one table, no copy here.

## Use when

Working with the `engine:containerd` provider, the plugin-nerdctl candy, or the
engine provider-class placement model.
