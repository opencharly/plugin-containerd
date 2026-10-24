package containerd

// verb.go — the `verb:containerd` READ-ONLY check verb (W3.9).
//
// The verb drives the venue's own tooling over the host's live executor reverse
// channel (sdk.Executor.RunCapture), so it measures the deployment rather than a
// fixture: every method answers a question only a running containerd can answer.
// The method set is deliberately small and each method can FAIL on a defect no
// other probe catches:
//
//	info            the daemon's identity and mode, from nerdctl info's
//	                SecurityOptions (a rootless daemon silently rerouting to the
//	                rootful socket is the failure this catches). Plugins is NEVER a
//	                mode source: it describes storage/log drivers, not privilege.
//	runtimes        the declared CRI runtime handlers (crictl info). A missing
//	                handler is why `--runtime krun` would silently fall back.
//	cdi_devices     the devices visible through the CLIENT's CDI spec dirs; a
//	                spec that is present but malformed must not pass as "no GPU".
//	criu_preflight  criu's seccomp_suspend feature check: rc 0 AND the supported
//	                line. The rc alone is not proof (criu check returns 0 for a
//	                feature list it did not fully verify), and --extra is never
//	                used — it changes the checks being run.
//	krun_guest      boots ONE throwaway probe guest and reads its kernel + CPU
//	                features, proving the krun handler actually boots a VM rather
//	                than a container.
//	image           an image is present in a named namespace (a rootless and a
//	                rootful daemon have disjoint stores).
//
// A capability the venue does not have is a VISIBLE skip naming the mechanism
// (crictl/criu absent, /dev/kvm absent, a runtime handler not declared, no probe
// image supplied), never a silent pass. An assertion that cannot run is not a
// green.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/opencharly/plugin-containerd/candy/plugin-containerd/params"
	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/shellquote"
	"github.com/opencharly/spec/spec"
	"go.yaml.in/yaml/v3"
)

// Method names — the #ContainerdInput.method closed set.
const (
	methodInfo          = "info"
	methodRuntimes      = "runtimes"
	methodCDIDevices    = "cdi_devices"
	methodCRIUPreflight = "criu_preflight"
	methodKrunGuest     = "krun_guest"
	methodImage         = "image"
)

// Defaults for method-exclusive modifiers.
const (
	defaultKrunRuntime = "krun"
	defaultImageNS     = "default"
)

// defaultCDISpecDirs are nerdctl's own client-side CDI spec directories. They are
// the CLIENT's dirs (what the nerdctl invocation will read), not the CRI plugin's
// server config.
var defaultCDISpecDirs = []string{"/etc/cdi", "/var/run/cdi"}

// venueExecutor is the slice of *sdk.Executor the check verbs use. It is an
// interface so every method is unit-testable against a fake venue without a
// go-plugin broker; the production path passes the real *sdk.Executor.
type venueExecutor interface {
	RunCapture(ctx context.Context, script string) (stdout, stderr string, exit int, err error)
}

// containerdVerbEnv is the plugin-side decode of the CheckEnv the host ships as
// Operation.Env for a `containerd:` check step (provider_checkenv.go).
type containerdVerbEnv struct {
	Box  string `json:"box"`
	Mode string `json:"mode"` // "live" | "box"
}

// invokeContainerdVerb serves the `verb:containerd` check verb: decode the full
// #Op + the desugared #ContainerdInput, skip in box mode (a `charly check box`
// container has no containerd daemon), run the method, and self-evaluate the
// authored exit/stdout/stderr matchers through the ONE shared verdict pipeline
// (sdk.VerbVerdict) — the out-of-process verb path does not run the host-side
// matcher pipeline, so this Invoke owns the whole verdict.
func invokeContainerdVerb(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	var op spec.Op
	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &op); err != nil {
			return sdk.ResultJSON("fail", "containerd: decode op: "+err.Error())
		}
	}
	var in params.ContainerdInput
	kit.DecodeInput(op.PluginInput, &in)
	method := in.Method
	if method == "" {
		return sdk.ResultJSON("fail", "containerd: missing method (want one of "+
			"info|runtimes|cdi_devices|criu_preflight|krun_guest|image)")
	}
	var env containerdVerbEnv
	if len(req.GetEnvJson()) > 0 {
		_ = json.Unmarshal(req.GetEnvJson(), &env)
	}
	if env.Mode == "box" {
		return sdk.ResultJSON("skip", fmt.Sprintf(
			"containerd: %s needs a running containerd daemon (skipped under charly check box)", method))
	}
	ex, err := sdk.ExecutorFromInvoke(req.GetExecutorBrokerId())
	if err != nil {
		return sdk.ResultJSON("fail", fmt.Sprintf(
			"containerd: %s has no host executor attached — the verb needs the live venue (%v)", method, err))
	}
	out, skip, runErr := dispatchVerb(ctx, ex, &in)
	if skip != "" {
		return sdk.ResultJSON("skip", skip)
	}
	return sdk.VerbVerdict("containerd", method, out, runErr, &op, false)
}

// dispatchVerb runs one method against the venue. A non-empty skip names the
// mechanism the venue does not have and outranks runErr; otherwise runErr is the
// assertion FAILING and is mapped through the authored matchers by VerbVerdict.
func dispatchVerb(ctx context.Context, ex venueExecutor, in *params.ContainerdInput) (out, skip string, err error) {
	switch in.Method {
	case methodInfo:
		out, err = verbInfo(ctx, ex, in)
	case methodRuntimes:
		out, skip, err = verbRuntimes(ctx, ex)
	case methodCDIDevices:
		out, err = verbCDIDevices(ctx, ex, in)
	case methodCRIUPreflight:
		out, skip, err = verbCRIUPreflight(ctx, ex)
	case methodKrunGuest:
		out, skip, err = verbKrunGuest(ctx, ex, in)
	case methodImage:
		out, err = verbImage(ctx, ex, in)
	default:
		err = fmt.Errorf("unknown containerd method %q", in.Method)
	}
	return out, skip, err
}

// nerdctlBase renders the nerdctl argv prefix, emitting --address/--namespace only
// when the caller supplied them: the capability defaults are never re-spelled.
func nerdctlBase(in *params.ContainerdInput) string {
	parts := []string{"nerdctl"}
	if in.Address != "" {
		parts = append(parts, "--address", shellquote.ShellQuote(in.Address))
	}
	if in.Namespace != "" {
		parts = append(parts, "--namespace", shellquote.ShellQuote(in.Namespace))
	}
	return strings.Join(parts, " ")
}

// capture runs one script on the venue and returns stdout/stderr/exit.
func capture(ctx context.Context, ex venueExecutor, script string) (string, string, int, error) {
	return ex.RunCapture(ctx, script)
}

// marshal encodes a method's report as the verdict body.
func marshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode report: %w", err)
	}
	return string(b), nil
}

// ---------------------------------------------------------------------------
// info
// ---------------------------------------------------------------------------

// nerdctlInfo is the subset of `nerdctl info --format {{json .}}` this verb reads.
// Plugins is deliberately NOT declared: the daemon mode comes from
// SecurityOptions, never from the storage/log plugin list.
type nerdctlInfo struct {
	ID              string   `json:"ID"`
	Name            string   `json:"Name"`
	ServerVersion   string   `json:"ServerVersion"`
	Driver          string   `json:"Driver"`
	OperatingSystem string   `json:"OperatingSystem"`
	Architecture    string   `json:"Architecture"`
	SecurityOptions []string `json:"SecurityOptions"`
}

type infoReport struct {
	DaemonID        string   `json:"daemon_id"`
	Mode            string   `json:"mode"`
	ServerVersion   string   `json:"server_version"`
	Driver          string   `json:"driver"`
	OperatingSystem string   `json:"operating_system"`
	Architecture    string   `json:"architecture"`
	Host            string   `json:"host"`
	SecurityOptions []string `json:"security_options"`
}

// verbInfo reads the daemon's identity and mode. Mode is derived ONLY from
// SecurityOptions: nerdctl reports `name=rootless` for a rootless daemon, and a
// rootless daemon that silently reroutes to the rootful socket is the defect this
// catches. An absent SecurityOptions list is a FAIL, never a guess from Plugins.
func verbInfo(ctx context.Context, ex venueExecutor, in *params.ContainerdInput) (string, error) {
	script := nerdctlBase(in) + " info --format '{{json .}}'"
	stdout, stderr, exit, err := capture(ctx, ex, script)
	if err != nil {
		return "", err
	}
	if exit != 0 {
		return "", fmt.Errorf("nerdctl info exited %d: %s", exit, firstLine(stderr+stdout))
	}
	var raw nerdctlInfo
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return "", fmt.Errorf("parse nerdctl info: %w", err)
	}
	if strings.TrimSpace(raw.ID) == "" {
		return "", errors.New("nerdctl info returned no daemon ID")
	}
	mode, err := daemonMode(raw.SecurityOptions)
	if err != nil {
		return "", err
	}
	return marshal(infoReport{
		DaemonID:        raw.ID,
		Mode:            mode,
		ServerVersion:   raw.ServerVersion,
		Driver:          raw.Driver,
		OperatingSystem: raw.OperatingSystem,
		Architecture:    raw.Architecture,
		Host:            raw.Name,
		SecurityOptions: raw.SecurityOptions,
	})
}

// daemonMode derives rootless/rootful from SecurityOptions ALONE. There is no
// Plugins fallback: Plugins lists storage/log drivers and says nothing about
// privilege, so inferring from it would be a fabricated answer.
func daemonMode(securityOptions []string) (string, error) {
	if len(securityOptions) == 0 {
		return "", errors.New("nerdctl info reported no SecurityOptions: the daemon mode is not derivable (Plugins is never a mode source)")
	}
	for _, opt := range securityOptions {
		if key, value, ok := strings.Cut(opt, "="); ok && key == "name" && value == "rootless" {
			return "rootless", nil
		}
	}
	return "rootful", nil
}

// ---------------------------------------------------------------------------
// runtimes
// ---------------------------------------------------------------------------

type runtimesReport struct {
	Runtimes []string `json:"runtimes"`
}

// verbRuntimes lists the CRI runtime handlers containerd declares. It uses
// `crictl info -o json`, whose config nests the handlers under a runtimes map;
// the map is located structurally so a cri-tools nesting change cannot silently
// produce an empty (passing) list.
func verbRuntimes(ctx context.Context, ex venueExecutor) (string, string, error) {
	stdout, stderr, exit, err := capture(ctx, ex, "crictl info -o json")
	if err != nil {
		return "", "", err
	}
	if exit != 0 {
		msg := firstLine(stderr + stdout)
		if missingBinary(msg, "crictl") {
			return "", "runtimes: crictl is not installed on the venue (the declared CRI runtime handlers are only reachable through crictl info)", nil
		}
		return "", "", fmt.Errorf("crictl info exited %d: %s", exit, msg)
	}
	var doc any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		return "", "", fmt.Errorf("parse crictl info: %w", err)
	}
	handlers := runtimesFromCRIConfig(doc)
	if len(handlers) == 0 {
		return "", "", errors.New("crictl info reported no config runtimes map (no runtime handler declared)")
	}
	out, err := marshal(runtimesReport{Runtimes: handlers})
	return out, "", err
}

// runtimesFromCRIConfig returns the SORTED union of every object-valued "runtimes"
// map in the crictl info document. cri-tools has nested the map under different
// config paths across releases; walking is stable across all of them, and a hit is
// never invented from a similarly-named scalar.
//
// The union (rather than the first hit) is deliberate: Go map iteration is
// randomized, so "first" would make a document carrying two runtimes maps
// non-deterministic. Collecting all names and sorting makes the answer independent
// of both map order and of how many maps cri-tools nests.
func runtimesFromCRIConfig(v any) []string {
	set := make(map[string]bool)
	collectRuntimes(v, set)
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// collectRuntimes accumulates every handler name under any object-valued
// "runtimes" key into set.
func collectRuntimes(v any, set map[string]bool) {
	switch node := v.(type) {
	case map[string]any:
		if raw, ok := node["runtimes"]; ok {
			if m, ok := raw.(map[string]any); ok {
				for name := range m {
					set[name] = true
				}
			}
		}
		for _, child := range node {
			collectRuntimes(child, set)
		}
	case []any:
		for _, child := range node {
			collectRuntimes(child, set)
		}
	}
}

// ---------------------------------------------------------------------------
// cdi_devices
// ---------------------------------------------------------------------------

type cdiSpec struct {
	CdiVersion string `yaml:"cdiVersion"`
	Kind       string `yaml:"kind"`
	Devices    []struct {
		Name string `yaml:"name"`
	} `yaml:"devices"`
}

type cdiReport struct {
	SpecDirs []string `json:"spec_dirs"`
	Devices  []string `json:"devices"`
	Specs    []string `json:"specs"`
}

// verbCDIDevices reads the CDI specs the nerdctl CLIENT will see: its own spec
// directories (/etc/cdi, /var/run/cdi by default, overridable per invocation).
// Reading the CRI plugin's server-side dirs instead would answer a different
// question — what the daemon sees, not what the client injects.
func verbCDIDevices(ctx context.Context, ex venueExecutor, in *params.ContainerdInput) (string, error) {
	dirs := in.Spec_dirs
	if len(dirs) == 0 {
		dirs = defaultCDISpecDirs
	}
	var script strings.Builder
	for _, dir := range dirs {
		q := shellquote.ShellQuote(dir)
		// The glob is intentionally unquoted; a missing directory leaves the
		// pattern literal and the [ -f ] test drops it. A malformed spec is a
		// FAIL below, never "no devices".
		fmt.Fprintf(&script, "for f in %s/*.yaml %s/*.yml %s/*.json; do [ -f \"$f\" ] || continue; echo \"===CDI $f\"; cat \"$f\"; done; ", q, q, q)
	}
	stdout, stderr, exit, err := capture(ctx, ex, script.String())
	if err != nil {
		return "", err
	}
	if exit != 0 {
		return "", fmt.Errorf("read CDI spec dirs exited %d: %s", exit, firstLine(stderr))
	}
	report := cdiReport{SpecDirs: dirs, Devices: []string{}, Specs: []string{}}
	for _, block := range strings.Split(stdout, "===CDI ") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		path, body, ok := strings.Cut(block, "\n")
		if !ok {
			return "", fmt.Errorf("CDI spec %s is empty", strings.TrimSpace(path))
		}
		var spec cdiSpec
		if err := yaml.Unmarshal([]byte(body), &spec); err != nil {
			return "", fmt.Errorf("parse CDI spec %s: %w", strings.TrimSpace(path), err)
		}
		if spec.Kind == "" {
			return "", fmt.Errorf("CDI spec %s has no kind", strings.TrimSpace(path))
		}
		report.Specs = append(report.Specs, strings.TrimSpace(path))
		for _, d := range spec.Devices {
			report.Devices = append(report.Devices, spec.Kind+"="+d.Name)
		}
	}
	sort.Strings(report.Devices)
	sort.Strings(report.Specs)
	return marshal(report)
}

// ---------------------------------------------------------------------------
// criu_preflight
// ---------------------------------------------------------------------------

type criuReport struct {
	ExitStatus int    `json:"exit_status"`
	Line       string `json:"seccomp_suspend_line"`
	Output     string `json:"output"`
}

// criuPreflightLine is the ONLY line that proves the feature. `criu check` also
// exits 0 when a requested feature was not verified, so the rc alone is not proof.
const criuPreflightLine = "seccomp_suspend is supported"

// verbCRIUPreflight runs criu's seccomp_suspend feature check. It requires BOTH
// rc 0 AND the supported line, and never passes --extra: --extra changes the set
// of checks (it enables optional/extended checks), so a run with it would prove a
// different claim than the one the bed makes.
func verbCRIUPreflight(ctx context.Context, ex venueExecutor) (string, string, error) {
	script := "criu check --unprivileged --feature seccomp_suspend 2>&1"
	stdout, _, exit, err := capture(ctx, ex, script)
	if err != nil {
		return "", "", err
	}
	if exit != 0 {
		if missingBinary(stdout, "criu") {
			return "", "criu_preflight: criu is not installed on the venue (the seccomp_suspend feature check cannot run)", nil
		}
		return "", "", fmt.Errorf("criu check exited %d: %s", exit, firstLine(stdout))
	}
	if !strings.Contains(stdout, criuPreflightLine) {
		return "", "", fmt.Errorf("criu check exited 0 but did not print %q: rc alone is not proof (output: %s)",
			criuPreflightLine, firstLine(stdout))
	}
	out, err := marshal(criuReport{ExitStatus: exit, Line: criuPreflightLine, Output: strings.TrimSpace(stdout)})
	return out, "", err
}

// ---------------------------------------------------------------------------
// krun_guest
// ---------------------------------------------------------------------------

type krunReport struct {
	Runtime  string   `json:"runtime"`
	Image    string   `json:"image"`
	Kernel   string   `json:"kernel"`
	Arch     string   `json:"arch"`
	KVM      string   `json:"dev_kvm"`
	Features []string `json:"cpu_features"`
}

// verbKrunGuest boots ONE throwaway probe guest (`--rm`) with the requested runtime
// handler and reads the guest's kernel, arch and CPU features. It proves the krun
// handler boots a VM rather than silently falling back to a container.
//
// Visible skips name the mechanism: no probe image supplied, no /dev/kvm on the
// venue, or the handler not declared by this containerd.
func verbKrunGuest(ctx context.Context, ex venueExecutor, in *params.ContainerdInput) (string, string, error) {
	if in.Image == "" {
		return "", "krun_guest: no probe image supplied (set image: to a locally available guest image)", nil
	}
	runtime := in.Runtime
	if runtime == "" {
		runtime = defaultKrunRuntime
	}
	stdout, stderr, exit, err := capture(ctx, ex, "test -e /dev/kvm && echo kvm-present || echo kvm-absent")
	if err != nil {
		return "", "", err
	}
	if exit != 0 {
		return "", "", fmt.Errorf("probe /dev/kvm exited %d: %s", exit, firstLine(stderr))
	}
	if strings.TrimSpace(stdout) != "kvm-present" {
		return "", "krun_guest: /dev/kvm is absent on the venue (no KVM, so no krun guest can boot)", nil
	}
	// The guest-side probe is a single sh -c: kernel, arch, CPU features, and
	// whether the guest itself got /dev/kvm.
	probe := "echo KERNEL=$(cat /proc/version); echo ARCH=$(uname -m); " +
		"echo FLAGS=$(grep -m1 '^flags' /proc/cpuinfo | cut -d: -f2); " +
		"echo KVM=$(test -e /dev/kvm && echo present || echo absent)"
	script := nerdctlBase(in) + " run --rm --runtime " + shellquote.ShellQuote(runtime) + " " +
		shellquote.ShellQuote(in.Image) + " sh -c " + shellquote.ShellQuote(probe)
	stdout, stderr, exit, err = capture(ctx, ex, script)
	if err != nil {
		return "", "", err
	}
	if exit != 0 {
		msg := stderr + stdout
		if handlerNotDeclared(msg, runtime) {
			return "", fmt.Sprintf("krun_guest: the %q runtime handler is not declared by this containerd (nerdctl: %s)", runtime, firstLine(msg)), nil
		}
		return "", "", fmt.Errorf("probe guest exited %d: %s", exit, firstLine(msg))
	}
	report := krunReport{Runtime: runtime, Image: in.Image}
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "KERNEL":
			report.Kernel = value
		case "ARCH":
			report.Arch = value
		case "KVM":
			report.KVM = value
		case "FLAGS":
			report.Features = strings.Fields(value)
		}
	}
	if report.Kernel == "" {
		return "", "", fmt.Errorf("probe guest printed no kernel (output: %s)", firstLine(stdout))
	}
	sort.Strings(report.Features)
	out, err := marshal(report)
	return out, "", err
}

// ---------------------------------------------------------------------------
// image
// ---------------------------------------------------------------------------

type imageReport struct {
	Image     string   `json:"image"`
	Namespace string   `json:"namespace"`
	ID        string   `json:"id"`
	RepoTags  []string `json:"repo_tags"`
	Arch      string   `json:"arch"`
	OS        string   `json:"os"`
}

// verbImage asserts an image is present in a NAMED namespace. Rootless and rootful
// daemons have disjoint stores, so "present" without the namespace is not a fact.
func verbImage(ctx context.Context, ex venueExecutor, in *params.ContainerdInput) (string, error) {
	if strings.TrimSpace(in.Image) == "" {
		return "", errors.New("image: the image reference is required")
	}
	ns := in.Namespace
	if ns == "" {
		ns = defaultImageNS
	}
	script := nerdctlBase(in) + " image inspect " + shellquote.ShellQuote(in.Image)
	stdout, stderr, exit, err := capture(ctx, ex, script)
	if err != nil {
		return "", err
	}
	if exit != 0 {
		return "", fmt.Errorf("image %s is not present in namespace %s: %s", in.Image, ns, firstLine(stderr+stdout))
	}
	var found []struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Arch     string   `json:"Architecture"`
		OS       string   `json:"Os"`
	}
	if err := json.Unmarshal([]byte(stdout), &found); err != nil {
		return "", fmt.Errorf("parse nerdctl image inspect: %w", err)
	}
	if len(found) == 0 {
		return "", fmt.Errorf("image %s is not present in namespace %s (empty inspect result)", in.Image, ns)
	}
	return marshal(imageReport{
		Image:     in.Image,
		Namespace: ns,
		ID:        found[0].ID,
		RepoTags:  found[0].RepoTags,
		Arch:      found[0].Arch,
		OS:        found[0].OS,
	})
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// firstLine returns the first non-empty line of s, bounded so a noisy command
// cannot flood a verdict message.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if len(t) > 300 {
				return t[:300] + "…"
			}
			return t
		}
	}
	return ""
}

// missingBinary reports whether a failure message says the named binary is absent.
func missingBinary(msg, binary string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, binary+": command not found") ||
		strings.Contains(lower, binary+": not found") ||
		strings.Contains(lower, "executable file not found") && strings.Contains(lower, binary)
}

// handlerNotDeclared reports whether a nerdctl run failed because the requested
// runtime handler is not configured. It matches ONLY handler-shaped refusals, so a
// real boot failure (kernel panic, no KVM, image error) is never downgraded to a
// skip.
func handlerNotDeclared(msg, runtime string) bool {
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, strings.ToLower(runtime)) && !strings.Contains(lower, "runtime") {
		return false
	}
	for _, phrase := range []string{
		"is not supported",
		"unknown runtime",
		"no such runtime",
		"runtime handler not found",
		"failed to get runtime",
		"failed to resolve runtime path",
		"invalid runtime name",
		"no runtime for",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}
