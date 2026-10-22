package nerdctl

// containerd_engine_test.go — the engine-word cutover contract.
//
// The spec module's engine vocabulary (schema/engine.cue #EngineName at
// v0.2026295.1243) is podman|docker|containerd, and `nerdctl` is DELETED as an
// engine word (it survives only as the CLI BINARY the containerd word is driven
// through, per #EngineBinaries). These tests FAIL while this plugin still serves
// the retired word and pass once it serves engine:containerd:
//
//   - TestProviderWordsAreContainerd             — the served capability words
//   - TestEngineWordIsContainerdBinaryIsNerdctl  — the word/binary split, from data
//   - TestStartPlanAlwaysCarriesRuntime          — never rely on nerdctl's default handler
//   - TestStartPlanOmitsDefaultAddressNamespace  — capability defaults are not re-spelled
//   - TestRootfulPlanUsesPrivilegePrefix         — rootful is NOT silently treated as rootless
//   - TestDescribeServesContainerdCapabilityRow  — the new capability fields over the wire
//   - TestOutOfProcessServeRoundTrip             — the real go-plugin boundary (slow)

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/go-plugin"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/ops"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
	"github.com/opencharly/spec/transport"
)

// providedWords returns class:word for every capability the plugin advertises.
func providedWords(t *testing.T) map[string]bool {
	t.Helper()
	caps, err := NewMeta().Describe(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	got := make(map[string]bool, len(caps.GetProvided()))
	for _, c := range caps.GetProvided() {
		got[c.GetClass()+":"+c.GetWord()] = true
	}
	return got
}

// TestProviderWordsAreContainerd pins the hard cutover: the plugin serves the
// engine:containerd word (plus its verb/command convenience words) and NOT the
// retired engine:nerdctl spelling. It is the negative arm: with the pre-cutover
// plugin it reports engine:nerdctl/verb:nerdctl/command:nerdctl.
func TestProviderWordsAreContainerd(t *testing.T) {
	got := providedWords(t)
	for _, want := range []string{"engine:containerd", "verb:containerd", "command:containerd"} {
		if !got[want] {
			t.Errorf("plugin does not advertise %s; got %v", want, got)
		}
	}
	for _, retired := range []string{"engine:nerdctl", "verb:nerdctl", "command:nerdctl"} {
		if got[retired] {
			t.Errorf("plugin still advertises the retired engine word %s", retired)
		}
	}
	if len(got) != 3 {
		t.Errorf("plugin advertises %d capabilities, want exactly 3: %v", len(got), got)
	}
}

// TestEngineWordIsContainerdBinaryIsNerdctl pins the word/binary split from the
// CUE-owned data: `containerd` is the engine WORD, `nerdctl` is only the binary,
// and the word is the one the closed spec vocabulary accepts.
func TestEngineWordIsContainerdBinaryIsNerdctl(t *testing.T) {
	if engineWord != "containerd" {
		t.Fatalf("engineWord = %q, want containerd (the closed engine vocabulary)", engineWord)
	}
	if !container.IsEngineName(engineWord) {
		t.Fatalf("%q is not a member of spec.EngineNames = %v", engineWord, spec.EngineNames)
	}
	if container.IsEngineName("nerdctl") {
		t.Fatalf("nerdctl is still a member of spec.EngineNames = %v", spec.EngineNames)
	}
	if got, want := container.EngineBinary(engineWord), spec.EngineBinaries["containerd"]; got != want {
		t.Fatalf("EngineBinary(%q) = %q, want the #EngineBinaries value %q", engineWord, got, want)
	}
	if spec.EngineBinaries["containerd"] != "nerdctl" {
		t.Fatalf("#EngineBinaries[containerd] = %q, want nerdctl", spec.EngineBinaries["containerd"])
	}
}

// invokeStartPlan calls the provider's engine start_plan op and decodes the reply.
func invokeStartPlan(t *testing.T, req spec.EngineStartPlanRequest) spec.EngineStartPlanReply {
	t.Helper()
	params, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal start_plan request: %v", err)
	}
	reply, err := NewProvider().Invoke(context.Background(), &pb.InvokeRequest{
		Reserved:   engineWord,
		Op:         ops.OpEngineStartPlan,
		ParamsJson: params,
	})
	if err != nil {
		t.Fatalf("start_plan invoke: %v", err)
	}
	var out spec.EngineStartPlanReply
	if err := json.Unmarshal(reply.GetResultJson(), &out); err != nil {
		t.Fatalf("decode start_plan reply: %v", err)
	}
	if out.Error != "" {
		t.Fatalf("start_plan reply error: %s", out.Error)
	}
	return out
}

// TestStartPlanAlwaysCarriesRuntime pins the policy that the containerd plan
// NEVER relies on the CLI's unqualified default handler: nerdctl's default is
// runc while the CRI default is crun, so an unqualified plan would silently run
// a different handler than the one the engine is configured for.
func TestStartPlanAlwaysCarriesRuntime(t *testing.T) {
	out := invokeStartPlan(t, spec.EngineStartPlanRequest{
		Name:   "charly-test",
		Image:  "docker.io/library/alpine:3.21",
		Detach: true,
	})
	if !hasFlagValue(out.Argv, "--runtime") {
		t.Fatalf("start_plan argv has no --runtime: %v", out.Argv)
	}
	if got := flagValue(out.Argv, "--runtime"); got != wantDefaultRuntime {
		t.Fatalf("--runtime = %q, want the named default %q", got, wantDefaultRuntime)
	}
	// The binary is the #EngineBinaries value, never a literal in the plugin.
	if got, want := out.Argv[0], spec.EngineBinaries["containerd"]; got != want {
		t.Fatalf("argv[0] = %q, want the #EngineBinaries binary %q", got, want)
	}
	// An explicitly requested handler is honoured unchanged.
	explicit := invokeStartPlan(t, spec.EngineStartPlanRequest{Name: "charly-test", Image: "img", Runtime: "runc"})
	if got := flagValue(explicit.Argv, "--runtime"); got != "runc" {
		t.Fatalf("explicit --runtime = %q, want runc", got)
	}
}

// TestStartPlanOmitsDefaultAddressNamespace pins that the plan does not re-spell
// the capability's DEFAULTS: no --address / --namespace is emitted unless the
// caller supplies an override (the start-plan envelope carries no such fields;
// an override rides extra_args and passes through unchanged).
func TestStartPlanOmitsDefaultAddressNamespace(t *testing.T) {
	out := invokeStartPlan(t, spec.EngineStartPlanRequest{Name: "charly-test", Image: "img"})
	for _, flag := range []string{"--address", "--namespace"} {
		if hasFlag(out.Argv, flag) {
			t.Errorf("default plan emitted %s: %v", flag, out.Argv)
		}
	}

	override := invokeStartPlan(t, spec.EngineStartPlanRequest{
		Name:      "charly-test",
		Image:     "img",
		ExtraArgs: []string{"--address", "/run/user/1000/containerd/containerd.sock", "--namespace", "k8s.io"},
	})
	for _, want := range []string{"/run/user/1000/containerd/containerd.sock", "k8s.io"} {
		if !containsArg(override.Argv, want) {
			t.Errorf("caller override %q did not pass through: %v", want, override.Argv)
		}
	}
}

// TestRootfulPlanUsesPrivilegePrefix pins that rootful is NOT silently treated
// as rootless: the rootful plan is prefixed with the capability's
// privilege_prefix (data), and the mode-specific workload-user fact applies.
func TestRootfulPlanUsesPrivilegePrefix(t *testing.T) {
	capability, ok := container.EngineCapabilityFor(engineWord)
	if !ok {
		t.Fatalf("no capability row for %q", engineWord)
	}
	if !capability.SupportsRootful {
		t.Fatalf("%q capability does not declare supports_rootful", engineWord)
	}

	rootful := invokeStartPlan(t, spec.EngineStartPlanRequest{Name: "charly-test", Image: "img", Rootful: true})
	prefix := strings.Fields(capability.PrivilegePrefix)
	if len(prefix) == 0 {
		t.Fatalf("%q declares supports_rootful with an empty privilege_prefix", engineWord)
	}
	if !reflect.DeepEqual(rootful.Argv[:len(prefix)], prefix) {
		t.Fatalf("rootful argv prefix = %v, want the capability privilege_prefix %v", rootful.Argv[:len(prefix)], prefix)
	}
	if got, want := rootful.Argv[len(prefix)], spec.EngineBinaries["containerd"]; got != want {
		t.Fatalf("rootful argv binary = %q, want %q", got, want)
	}
	if hasFlag(rootful.Argv, "--user") {
		t.Errorf("rootful plan forced a workload user (workload_user_rootful is empty): %v", rootful.Argv)
	}

	rootless := invokeStartPlan(t, spec.EngineStartPlanRequest{Name: "charly-test", Image: "img"})
	if !reflect.DeepEqual(rootless.Argv[:1], []string{spec.EngineBinaries["containerd"]}) {
		t.Fatalf("rootless argv does not start with the binary: %v", rootless.Argv)
	}
	if got, want := flagValue(rootless.Argv, "--user"), capability.WorkloadUserRootless; got != want {
		t.Errorf("rootless --user = %q, want workload_user_rootless %q", got, want)
	}
}

// TestDescribeServesContainerdCapabilityRow pins every field the spec cutover
// added: the plugin serves the capability row over the engine describe op, from
// the ONE spec table, never a second copy.
func TestDescribeServesContainerdCapabilityRow(t *testing.T) {
	reply, err := NewProvider().Invoke(context.Background(), &pb.InvokeRequest{
		Reserved: engineWord,
		Op:       ops.OpEngineDescribe,
	})
	if err != nil {
		t.Fatalf("describe invoke: %v", err)
	}
	var out spec.EngineDescribeReply
	if err := json.Unmarshal(reply.GetResultJson(), &out); err != nil {
		t.Fatalf("decode describe reply: %v", err)
	}
	if out.Error != "" || out.Capability == nil {
		t.Fatalf("describe reply = %+v", out)
	}
	c := *out.Capability
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"name", c.Name, spec.EngineName("containerd")},
		{"binary", c.Binary, spec.EngineBinaries["containerd"]},
		{"supports_rootless", c.SupportsRootless, true},
		{"supports_rootful", c.SupportsRootful, true},
		{"workload_user_rootless", c.WorkloadUserRootless, "0"},
		{"workload_user_rootful", c.WorkloadUserRootful, ""},
		{"namespace", c.Namespace, "default"},
		{"substrates", c.Substrates, []string{"pod"}},
	}
	for _, chk := range checks {
		if !reflect.DeepEqual(chk.got, chk.want) {
			t.Errorf("capability %s = %#v, want %#v", chk.name, chk.got, chk.want)
		}
	}
	if c.Address == "" {
		t.Error("capability address is empty")
	}
	if c.PrivilegePrefix == "" {
		t.Error("capability privilege_prefix is empty")
	}
	for _, handler := range []string{"crun", "krun", "runc", "nvidia"} {
		if !containsArg(c.RuntimeHandlers, handler) {
			t.Errorf("capability runtime_handlers %v missing %q", c.RuntimeHandlers, handler)
		}
	}
}

// TestOutOfProcessServeRoundTrip exercises the REAL placement: it host-builds
// cmd/serve and connects to it over go-plugin gRPC (the same handshake +
// dispense key charly uses), then reads Describe and invokes start_plan across
// the process boundary. Skipped with -short (it builds + execs a binary).
func TestOutOfProcessServeRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds + execs the plugin binary (slow)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "serve")
	build := exec.Command("go", "build", "-o", bin, "./cmd/serve")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/serve: %v\n%s", err, out)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CHARLY_PLUGIN=1")
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  transport.Handshake,
		Plugins:          transport.PluginMap(nil, nil),
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		AutoMTLS:         true,
	})
	defer client.Kill()

	rpc, err := client.Client()
	if err != nil {
		t.Fatalf("plugin client: %v", err)
	}
	raw, err := rpc.Dispense(transport.DispenseKey)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	conn, ok := raw.(*transport.Conn)
	if !ok {
		t.Fatalf("dispensed %T, want *transport.Conn", raw)
	}
	ctx := context.Background()
	caps, err := conn.Meta.Describe(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("out-of-process Describe: %v", err)
	}
	var words []string
	for _, c := range caps.GetProvided() {
		words = append(words, c.GetClass()+":"+c.GetWord())
	}
	if !containsArg(words, "engine:containerd") || containsArg(words, "engine:nerdctl") {
		t.Fatalf("out-of-process capabilities = %v, want engine:containerd and no engine:nerdctl", words)
	}

	params, _ := json.Marshal(spec.EngineStartPlanRequest{Name: "charly-test", Image: "img"})
	rep, err := conn.Provider.Invoke(ctx, &pb.InvokeRequest{
		Reserved:   engineWord,
		Op:         ops.OpEngineStartPlan,
		ParamsJson: params,
	})
	if err != nil {
		t.Fatalf("out-of-process start_plan: %v", err)
	}
	var plan spec.EngineStartPlanReply
	if err := json.Unmarshal(rep.GetResultJson(), &plan); err != nil {
		t.Fatalf("decode out-of-process start_plan: %v", err)
	}
	if got := flagValue(plan.Argv, "--runtime"); got != wantDefaultRuntime {
		t.Fatalf("out-of-process --runtime = %q, want %q (argv %v)", got, wantDefaultRuntime, plan.Argv)
	}
}

// wantDefaultRuntime is the handler the plan MUST name when the caller names
// none: nerdctl's unqualified default is runc, while the CRI default is crun.
const wantDefaultRuntime = "crun"

// --- small argv helpers (test-local) ---

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	return flagValue(args, flag) != "" || containsArg(args, flag)
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

func hasFlagValue(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}
