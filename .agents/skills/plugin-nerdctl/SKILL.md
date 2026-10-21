---
name: plugin-nerdctl
description: The out-of-tree charly ENGINE plugin serving engine:nerdctl (verb:nerdctl
  / command:nerdctl) — every engine op delegates to container.InvokeEngineOp. Use
  when working with the engine:nerdctl provider, the plugin-nerdctl candy, or the
  engine provider-class placement model.
---

# plugin-nerdctl

The out-of-tree charly ENGINE plugin serving `engine:nerdctl` (plus the
`verb:nerdctl` / `command:nerdctl` convenience words).

## What it is

A standalone Go module (`go.mod` + `cmd/serve`) served over go-plugin gRPC via
the charly plugin SDK. The charly loader host-builds `./cmd/serve` and connects
it OUT-OF-PROCESS; the SAME `NewProvider()`/`NewMeta()` compile INTO charly
in-process when listed in `compiled_plugins` — placement is invisible above the
registry.

## The one body, two placements

Every engine op (`describe` / `binary` / `gpu_args` / `start_plan` /
`unit_emit` / `network_ensure`) is answered by delegating to the pure body
`container.InvokeEngineOp("nerdctl", …)` in the spec module — the SAME body the
compiled-in `engine:podman` / `engine:docker` providers serve. So the engine
class has one implementation and two placements.

## Use when

Working with the `engine:nerdctl` provider, the plugin-nerdctl candy, or the
engine provider-class placement model.
