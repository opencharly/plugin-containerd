// Package nerdctl is the out-of-tree charly ENGINE plugin serving the `engine`
// provider class for the containerd word — `engine:containerd` — plus the
// `verb:containerd` / `command:containerd` convenience words.
//
// The ENGINE WORD is `containerd`; `nerdctl` is only the CLI BINARY that drives
// it (the CUE-owned #EngineBinaries map, spec.EngineBinaries["containerd"]), and
// never an engine word. The spec module's closed vocabulary is podman | docker |
// containerd; the retired engine spelling is deleted (R5, no alias).
//
// The engine provider is engine-word-keyed DATA + ONE dispatch: this plugin
// answers every engine op (describe / binary / gpu_args / start_plan / unit_emit /
// network_ensure) by delegating to the pure body `container.InvokeEngineOp(engineWord, …)`
// in the spec module, so the op behavior is written ONCE and served identically by
// the compiled-in `engine:podman` / `engine:docker` providers and this
// out-of-process `engine:containerd` plugin.
//
// The one containerd-SPECIFIC policy lives here, on the provider that serves the
// containerd row, and is applied to the start plan BEFORE/AFTER the pure body:
//   - a runtime handler is ALWAYS named (nerdctl's unqualified default handler is
//     runc while the CRI default is crun — a plan must never rely on a default);
//   - a rootful plan is prefixed with the capability's `privilege_prefix`
//     (e.g. `sudo -n`), so rootful is never silently treated as rootless.
//
// Dual-placement by construction: the SAME NewProvider()/NewMeta() compile INTO
// charly in-process when listed in compiled_plugins, or cmd/serve serves them
// OUT-OF-PROCESS over go-plugin gRPC when they are not — placement is invisible
// above the registry.
package nerdctl

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/ops"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

//go:embed schema/*.cue
var schemaFS embed.FS

const calver = "2026.295.1306"

// engineWord is both the registry key ("engine:containerd") and the engine WORD
// the pure body dispatches on. `nerdctl` is NOT an engine word: it is the CLI
// binary #EngineBinaries maps the containerd word to.
const engineWord = "containerd"

// defaultRuntimeHandler is the OCI runtime handler a containerd start plan names
// when the caller names none. nerdctl's unqualified default handler is runc,
// while the CRI default is crun, so an unqualified plan would silently select a
// different handler than the engine is configured for. It is validated against
// the capability's CUE-owned runtime_handlers before use — never assumed.
const defaultRuntimeHandler = "crun"

// NewProvider returns the containerd provider.
func NewProvider() pb.ProviderServer { return &provider{} }

// NewMeta advertises the engine:containerd capability (empty InputDef — the engine
// class carries no authored plugin_input; its envelopes live in the base
// schema/spec) plus the verb/command convenience words, together with the
// plugin's OWN self-contained, package-less CUE schema (schema/plugin.cue) served
// over Describe: there is NO schema-less plugin.
func NewMeta() pb.PluginMetaServer {
	return sdk.NewMeta(calver,
		[]sdk.ProvidedCapability{
			{Class: "engine", Word: engineWord, InputDef: ""},
			{Class: "verb", Word: engineWord, InputDef: ""},
			{Class: "command", Word: engineWord, InputDef: ""},
		},
		schemaFS)
}

type provider struct{ pb.UnimplementedProviderServer }

// Invoke answers every engine op by delegating to the pure fabric servant
// container.InvokeEngineOp. The start_plan op is the one op carrying the
// containerd row's own policy (an always-named runtime handler and the rootful
// privilege prefix); every other op is a direct delegation. The verb/command
// words serve the same envelopes.
func (provider) Invoke(_ context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	op := req.GetOp()
	params := json.RawMessage(req.GetParamsJson())
	var (
		out json.RawMessage
		err error
	)
	if op == ops.OpEngineStartPlan {
		out, err = containerdStartPlan(params)
	} else {
		out, err = container.InvokeEngineOp(engineWord, op, params)
	}
	if err != nil {
		return nil, fmt.Errorf("plugin-nerdctl: engine:%s op %q: %w", engineWord, op, err)
	}
	return &pb.InvokeReply{ResultJson: out}, nil
}

// containerdStartPlan applies the containerd row's start-plan policy and then
// delegates the argv assembly to the ONE engine body:
//
//  1. a rootful request is checked against the capability (a rootful target must
//     be declared) and the workload-user fact the pure body uses is the rootful
//     one;
//  2. an empty runtime is filled with defaultRuntimeHandler, validated against
//     the capability's runtime_handlers, so `--runtime` is ALWAYS emitted;
//  3. the pure body builds the argv;
//  4. a rootful plan is prefixed with the capability's privilege_prefix as argv
//     words (`sudo -n`), taken from data — never a hard-coded escalation. An
//     empty prefix means the invoking user already reaches the rootful daemon.
//
// The capability DEFAULTS (address, namespace) are never re-spelled here: a
// default flag is not emitted, and a caller's override rides `extra_args`
// through the pure body unchanged.
func containerdStartPlan(params json.RawMessage) (json.RawMessage, error) {
	var req spec.EngineStartPlanRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("start_plan: %w", err)
		}
	}
	capability, ok := container.EngineCapabilityFor(engineWord)
	if !ok {
		return nil, fmt.Errorf("engine %q has no capability row", engineWord)
	}
	if req.Rootful && !capability.SupportsRootful {
		return nil, fmt.Errorf("engine %q does not support rootful targets", engineWord)
	}
	if req.Runtime == "" {
		if !slices.Contains(capability.RuntimeHandlers, defaultRuntimeHandler) {
			return nil, fmt.Errorf("engine %q does not declare the default runtime handler %q (handlers: %v)",
				engineWord, defaultRuntimeHandler, capability.RuntimeHandlers)
		}
		req.Runtime = defaultRuntimeHandler
	}
	adjusted, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("start_plan: %w", err)
	}
	out, err := container.InvokeEngineOp(engineWord, ops.OpEngineStartPlan, adjusted)
	if err != nil {
		return nil, err
	}
	if !req.Rootful {
		return out, nil
	}
	var reply spec.EngineStartPlanReply
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("start_plan reply: %w", err)
	}
	if reply.Error != "" {
		return out, nil
	}
	prefix := strings.Fields(capability.PrivilegePrefix)
	if len(prefix) == 0 {
		return out, nil
	}
	reply.Argv = append(prefix, reply.Argv...)
	prefixed, err := json.Marshal(reply)
	if err != nil {
		return nil, fmt.Errorf("start_plan reply: %w", err)
	}
	return prefixed, nil
}
