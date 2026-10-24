---
name: plugin-containerd
description: The out-of-tree charly ENGINE plugin serving engine:containerd (verb:containerd
  / command:containerd) — every engine op delegates to container.InvokeEngineOp. `containerd`
  is the engine word; `nerdctl` is only the CLI binary it is driven through. Use when working
  with the engine:containerd provider, the plugin-containerd candy, or the engine provider-class
  placement model.
---

# plugin-containerd

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
  `privilege_prefix` (e.g. `sudo -n`) as an argv prefix, taken from data.
  `unit_emit` is a pure renderer and its envelope carries no scope field: it
  renders a user unit from the `start_argv` it is given. A rootful unit carries
  the prefix and `--runtime` only when the caller passes the rootful start-plan
  argv; the provider does not re-apply the policy at the unit boundary.
- **Defaults are not re-spelled.** No `--address` / `--namespace` is emitted for
  the capability defaults; a caller's override rides `extra_args`.

The capability row itself (`supports_rootless` / `supports_rootful`, the
mode-split workload users, `address`, `namespace`, `privilege_prefix`,
`substrates`, `runtime_handlers`) is served by the spec module's engine body —
one table, no copy here.

## The read-only check verb (`verb:containerd`)

`containerd: <method>` is a READ-ONLY check verb (W3.9). Its typed input is
`#ContainerdInput` with `method` as the PRIMARY field, so `containerd: info`
desugars to `{method: "info"}`. It drives the venue's own tooling over the host
executor reverse channel; each method catches a defect nothing else does:

| method | measures | visible skip (names the mechanism) |
|---|---|---|
| `info` | daemon id + mode from `nerdctl info` **SecurityOptions** (never `Plugins`) | — |
| `runtimes` | the declared CRI handlers (`crictl info`) | `crictl` not installed |
| `cdi_devices` | devices in the **client's** CDI spec dirs (`/etc/cdi`, `/var/run/cdi`) | — (an absent dir is an empty, passing list; a malformed spec FAILS) |
| `criu_preflight` | `criu check --unprivileged --feature seccomp_suspend`: rc **and** the supported line; `--extra` is never used | `criu` not installed |
| `krun_guest` | one throwaway probe guest (`--rm`): kernel, arch, CPU features | no probe `image:`, no `/dev/kvm`, handler not declared |
| `image` | an image present in a **named** namespace | — |

A skip is never a silent pass: an assertion that cannot run is not green.

## Use when

Working with the `engine:containerd` provider, the `containerd:` check verb, the
plugin-containerd candy, or the engine provider-class placement model.
