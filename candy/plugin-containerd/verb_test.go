package containerd

// verb_test.go — the `verb:containerd` check-verb contract (W3.9).
//
// Every method has a deterministic arm against a FAKE venue (the failure is
// visible: each test names the defect it catches) plus one LIVE arm at the end
// that runs against the real daemon, or skips VISIBLY naming the mechanism it
// lacks. The fake venue is the host-executor seam (venueExecutor), so no
// go-plugin broker is needed here; the live arm's localVenue is a test-only
// stand-in for the host executor on a `host: local` venue.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/opencharly/plugin-containerd/candy/plugin-containerd/params"
	pb "github.com/opencharly/spec/proto"
)

// fakeVenue answers RunCapture from a script→response table and records every
// script so a test can assert the argv the verb actually built.
type fakeVenue struct {
	scripts []string
	answer  func(script string) (stdout, stderr string, exit int, err error)
}

func (f *fakeVenue) RunCapture(_ context.Context, script string) (string, string, int, error) {
	f.scripts = append(f.scripts, script)
	if f.answer == nil {
		return "", "", 0, nil
	}
	return f.answer(script)
}

func (f *fakeVenue) lastScript() string {
	if len(f.scripts) == 0 {
		return ""
	}
	return f.scripts[len(f.scripts)-1]
}

func fixed(stdout, stderr string, exit int) func(string) (string, string, int, error) {
	return func(string) (string, string, int, error) { return stdout, stderr, exit, nil }
}

// TestVerbInfoDerivesModeFromSecurityOptionsOnly pins the rule that the daemon
// mode comes from SecurityOptions, NEVER from Plugins. The third arm is the
// negative control: a Plugins-rich document with no SecurityOptions must FAIL
// instead of inferring a mode.
func TestVerbInfoDerivesModeFromSecurityOptionsOnly(t *testing.T) {
	rootless := &fakeVenue{answer: fixed(`{"ID":"daemon-1","SecurityOptions":["name=seccomp,profile=builtin","name=rootless"]}`, "", 0)}
	out, skip, err := dispatchVerb(context.Background(), rootless, &params.ContainerdInput{Method: methodInfo})
	if err != nil || skip != "" {
		t.Fatalf("rootless info: err=%v skip=%q", err, skip)
	}
	var rep infoReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode info report: %v", err)
	}
	if rep.Mode != "rootless" {
		t.Fatalf("mode = %q, want rootless", rep.Mode)
	}
	if rep.DaemonID != "daemon-1" {
		t.Fatalf("daemon id = %q, want daemon-1", rep.DaemonID)
	}
	if !strings.Contains(rootless.lastScript(), "info --format") {
		t.Fatalf("info script does not call nerdctl info --format: %q", rootless.lastScript())
	}

	rootful := &fakeVenue{answer: fixed(`{"ID":"daemon-2","SecurityOptions":["name=seccomp"]}`, "", 0)}
	out, _, err = dispatchVerb(context.Background(), rootful, &params.ContainerdInput{Method: methodInfo})
	if err != nil {
		t.Fatalf("rootful info: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode rootful report: %v", err)
	}
	if rep.Mode != "rootful" {
		t.Fatalf("mode = %q, want rootful", rep.Mode)
	}

	pluginsOnly := &fakeVenue{answer: fixed(`{"ID":"daemon-3","Plugins":{"Storage":["overlayfs"],"Log":["json-file"]}}`, "", 0)}
	if _, _, err := dispatchVerb(context.Background(), pluginsOnly, &params.ContainerdInput{Method: methodInfo}); err == nil {
		t.Fatal("info passed a document with no SecurityOptions: mode must never be inferred from Plugins")
	}
}

// TestVerbRuntimesListsDeclaredHandlers pins the crictl-based handler report and
// its visible skip when crictl is absent (the mechanism, not a green).
func TestVerbRuntimesListsDeclaredHandlers(t *testing.T) {
	venue := &fakeVenue{answer: fixed(`{"config":{"containerd":{"runtimes":{"krun":{},"crun":{}}}}}`, "", 0)}
	out, skip, err := dispatchVerb(context.Background(), venue, &params.ContainerdInput{Method: methodRuntimes})
	if err != nil || skip != "" {
		t.Fatalf("runtimes: err=%v skip=%q", err, skip)
	}
	var rep runtimesReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode runtimes report: %v", err)
	}
	if got, want := strings.Join(rep.Runtimes, ","), "crun,krun"; got != want {
		t.Fatalf("handlers = %q, want %q (sorted)", got, want)
	}

	absent := &fakeVenue{answer: fixed("", "crictl: command not found", 127)}
	_, skip, err = dispatchVerb(context.Background(), absent, &params.ContainerdInput{Method: methodRuntimes})
	if err != nil {
		t.Fatalf("absent crictl must be a skip, got err=%v", err)
	}
	if !strings.Contains(skip, "crictl") {
		t.Fatalf("skip does not name crictl: %q", skip)
	}

	empty := &fakeVenue{answer: fixed(`{"config":{}}`, "", 0)}
	if _, _, err := dispatchVerb(context.Background(), empty, &params.ContainerdInput{Method: methodRuntimes}); err == nil {
		t.Fatal("runtimes passed a config with no handlers map")
	}
}

// TestVerbCDIDevicesReadsClientSpecDirs pins that the verb reads the CLIENT's
// spec directories (default /etc/cdi + /var/run/cdi, overridable), not a
// server-side config, and that a malformed spec FAILS instead of reading as "no
// devices".
func TestVerbCDIDevicesReadsClientSpecDirs(t *testing.T) {
	specs := "" +
		"===CDI /etc/cdi/vendor.yaml\n" +
		"cdiVersion: 0.5.0\nkind: example.com/gpu\ndevices:\n  - name: \"1\"\n" +
		"===CDI /var/run/cdi/gpu.json\n" +
		"{\"cdiVersion\":\"0.5.0\",\"kind\":\"example.com/gpu\",\"devices\":[{\"name\":\"0\"}]}\n"
	venue := &fakeVenue{answer: fixed(specs, "", 0)}
	out, skip, err := dispatchVerb(context.Background(), venue, &params.ContainerdInput{Method: methodCDIDevices})
	if err != nil || skip != "" {
		t.Fatalf("cdi_devices: err=%v skip=%q", err, skip)
	}
	var rep cdiReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode cdi report: %v", err)
	}
	if got, want := strings.Join(rep.Devices, ","), "example.com/gpu=0,example.com/gpu=1"; got != want {
		t.Fatalf("devices = %q, want %q", got, want)
	}
	script := venue.lastScript()
	for _, dir := range []string{"/etc/cdi", "/var/run/cdi"} {
		if !strings.Contains(script, dir) {
			t.Fatalf("script does not read the client spec dir %s: %q", dir, script)
		}
	}

	custom := &fakeVenue{answer: fixed("", "", 0)}
	if _, _, err := dispatchVerb(context.Background(), custom, &params.ContainerdInput{
		Method: methodCDIDevices, Spec_dirs: []string{"/opt/cdi"},
	}); err != nil {
		t.Fatalf("custom spec dirs: %v", err)
	}
	if !strings.Contains(custom.lastScript(), "/opt/cdi") || strings.Contains(custom.lastScript(), "/etc/cdi") {
		t.Fatalf("custom spec_dirs not honoured: %q", custom.lastScript())
	}

	malformed := &fakeVenue{answer: fixed("===CDI /etc/cdi/broken.yaml\ncdiVersion: [not-a-scalar\n", "", 0)}
	if _, _, err := dispatchVerb(context.Background(), malformed, &params.ContainerdInput{Method: methodCDIDevices}); err == nil {
		t.Fatal("cdi_devices passed a malformed spec (a broken spec must not read as no devices)")
	}
}

// TestVerbCRIURequiresRCAndLine pins the preflight contract: rc 0 alone is not
// proof; the supported line is required too; and --extra is NEVER used (it
// changes the checks being run, so the claim would be about a different run).
func TestVerbCRIURequiresRCAndLine(t *testing.T) {
	good := &fakeVenue{answer: fixed("seccomp_suspend is supported\n", "", 0)}
	out, skip, err := dispatchVerb(context.Background(), good, &params.ContainerdInput{Method: methodCRIUPreflight})
	if err != nil || skip != "" {
		t.Fatalf("criu_preflight: err=%v skip=%q", err, skip)
	}
	var rep criuReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode criu report: %v", err)
	}
	if rep.ExitStatus != 0 || rep.Line != criuPreflightLine {
		t.Fatalf("report = %+v, want rc 0 + the supported line", rep)
	}
	if strings.Contains(good.lastScript(), "--extra") {
		t.Fatalf("criu_preflight used --extra: %q", good.lastScript())
	}

	rcOnly := &fakeVenue{answer: fixed("seccomp_suspend: failed\n", "", 0)}
	if _, _, err := dispatchVerb(context.Background(), rcOnly, &params.ContainerdInput{Method: methodCRIUPreflight}); err == nil {
		t.Fatal("criu_preflight passed on rc 0 without the supported line")
	}

	lineOnly := &fakeVenue{answer: fixed("seccomp_suspend is supported\n", "", 1)}
	if _, _, err := dispatchVerb(context.Background(), lineOnly, &params.ContainerdInput{Method: methodCRIUPreflight}); err == nil {
		t.Fatal("criu_preflight passed with a non-zero rc")
	}

	absent := &fakeVenue{answer: fixed("criu: command not found\n", "", 127)}
	_, skip, err = dispatchVerb(context.Background(), absent, &params.ContainerdInput{Method: methodCRIUPreflight})
	if err != nil {
		t.Fatalf("absent criu must be a skip, got err=%v", err)
	}
	if !strings.Contains(skip, "criu") {
		t.Fatalf("skip does not name criu: %q", skip)
	}
}

// TestVerbKrunGuestBootsOrSkipsVisibly pins the probe-guest arm: it needs an
// image, /dev/kvm, and a declared handler; each absence is a visible skip naming
// the mechanism, while a real boot failure stays a FAIL.
func TestVerbKrunGuestBootsOrSkipsVisibly(t *testing.T) {
	noImage := &fakeVenue{}
	_, skip, err := dispatchVerb(context.Background(), noImage, &params.ContainerdInput{Method: methodKrunGuest})
	if err != nil || !strings.Contains(skip, "image") {
		t.Fatalf("no image: skip=%q err=%v, want a skip naming the missing probe image", skip, err)
	}

	noKVM := &fakeVenue{answer: func(script string) (string, string, int, error) {
		if strings.Contains(script, "/dev/kvm") && strings.HasPrefix(script, "test -e") {
			return "kvm-absent\n", "", 0, nil
		}
		return "", "", 0, nil
	}}
	_, skip, err = dispatchVerb(context.Background(), noKVM, &params.ContainerdInput{Method: methodKrunGuest, Image: "probe:latest"})
	if err != nil || !strings.Contains(skip, "/dev/kvm") {
		t.Fatalf("no kvm: skip=%q err=%v, want a skip naming /dev/kvm", skip, err)
	}

	// The exact refusal measured live on nerdctl 2.3.5 (2026-10-24): a short
	// handler alias that containerd never declared. It is a configuration
	// capability the venue lacks, so it must be a skip, not a fail.
	undeclared := &fakeVenue{answer: func(script string) (string, string, int, error) {
		if strings.HasPrefix(script, "test -e /dev/kvm") {
			return "kvm-present\n", "", 0, nil
		}
		return "", "time=\"…\" level=fatal msg=\"failed to start shim: failed to resolve runtime path: invalid runtime name krun, correct runtime name should be either format like `io.containerd.runc.v2` or a full path to the binary\"", 1, nil
	}}
	_, skip, err = dispatchVerb(context.Background(), undeclared, &params.ContainerdInput{Method: methodKrunGuest, Image: "probe:latest"})
	if err != nil || !strings.Contains(skip, "krun") {
		t.Fatalf("undeclared handler: skip=%q err=%v, want a skip naming the handler", skip, err)
	}

	// A REAL boot failure must stay a FAIL: it is not downgraded to a skip.
	realFailure := &fakeVenue{answer: func(script string) (string, string, int, error) {
		if strings.HasPrefix(script, "test -e /dev/kvm") {
			return "kvm-present\n", "", 0, nil
		}
		return "", "kernel panic: no working init found", 1, nil
	}}
	if _, _, err := dispatchVerb(context.Background(), realFailure, &params.ContainerdInput{Method: methodKrunGuest, Image: "probe:latest"}); err == nil {
		t.Fatal("a genuine guest boot failure must FAIL, not skip")
	}

	booted := &fakeVenue{answer: func(script string) (string, string, int, error) {
		if strings.HasPrefix(script, "test -e /dev/kvm") {
			return "kvm-present\n", "", 0, nil
		}
		return "KERNEL=Linux version 6.12.0-krun\nARCH=x86_64\nFLAGS=fpu sse\nKVM=present\n", "", 0, nil
	}}
	out, skip, err := dispatchVerb(context.Background(), booted, &params.ContainerdInput{Method: methodKrunGuest, Image: "probe:latest"})
	if err != nil || skip != "" {
		t.Fatalf("booted guest: err=%v skip=%q", err, skip)
	}
	var rep krunReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode krun report: %v", err)
	}
	if rep.Kernel != "Linux version 6.12.0-krun" || rep.Arch != "x86_64" || rep.KVM != "present" {
		t.Fatalf("krun report = %+v", rep)
	}
	if got := strings.Join(rep.Features, ","); got != "fpu,sse" {
		t.Fatalf("features = %q, want sorted fpu,sse", got)
	}
	runScript := booted.lastScript()
	for _, want := range []string{"run --rm", "--runtime 'krun'", "probe:latest"} {
		if !strings.Contains(runScript, want) {
			t.Fatalf("probe script missing %q: %q", want, runScript)
		}
	}
}

// TestVerbImageIsNamespaced pins that "present" is answered in a NAMED namespace
// (rootless and rootful stores are disjoint) and that an absent image FAILS.
func TestVerbImageIsNamespaced(t *testing.T) {
	present := &fakeVenue{answer: fixed(`[{"Id":"sha256:abc","RepoTags":["probe:latest"],"Architecture":"amd64","Os":"linux"}]`, "", 0)}
	out, skip, err := dispatchVerb(context.Background(), present, &params.ContainerdInput{Method: methodImage, Image: "probe:latest"})
	if err != nil || skip != "" {
		t.Fatalf("image: err=%v skip=%q", err, skip)
	}
	var rep imageReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode image report: %v", err)
	}
	if rep.Namespace != "default" || rep.ID != "sha256:abc" {
		t.Fatalf("image report = %+v, want namespace default + the image id", rep)
	}

	k8s := &fakeVenue{answer: fixed(`[{"Id":"sha256:abc"}]`, "", 0)}
	if _, _, err := dispatchVerb(context.Background(), k8s, &params.ContainerdInput{
		Method: methodImage, Image: "probe:latest", Namespace: "k8s.io",
	}); err != nil {
		t.Fatalf("namespaced image: %v", err)
	}
	if !strings.Contains(k8s.lastScript(), "--namespace 'k8s.io'") {
		t.Fatalf("image script did not pass the namespace: %q", k8s.lastScript())
	}

	absent := &fakeVenue{answer: fixed("", "no such image", 1)}
	if _, _, err := dispatchVerb(context.Background(), absent, &params.ContainerdInput{
		Method: methodImage, Image: "absent:latest", Namespace: "k8s.io",
	}); err == nil || !strings.Contains(err.Error(), "k8s.io") {
		t.Fatalf("absent image must fail naming the namespace; err=%v", err)
	}

	noRef := &fakeVenue{}
	if _, _, err := dispatchVerb(context.Background(), noRef, &params.ContainerdInput{Method: methodImage}); err == nil {
		t.Fatal("image passed without an image reference")
	}
}

// TestVerbCapabilityDeclaresTypedInput pins the served capability: the host can
// only desugar `containerd: info` when the verb advertises #ContainerdInput with
// `method` as PRIMARY.
func TestVerbCapabilityDeclaresTypedInput(t *testing.T) {
	caps := NewMeta()
	desc, err := caps.Describe(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	var found bool
	for _, c := range desc.GetProvided() {
		if c.GetClass() == "verb" && c.GetWord() == engineWord {
			found = true
			if c.GetInputDef() != "#ContainerdInput" {
				t.Fatalf("verb input def = %q, want #ContainerdInput", c.GetInputDef())
			}
			if c.GetPrimary() != "method" {
				t.Fatalf("verb primary = %q, want method", c.GetPrimary())
			}
		}
	}
	if !found {
		t.Fatal("plugin does not advertise verb:containerd")
	}
}

// ---------------------------------------------------------------------------
// LIVE arm
// ---------------------------------------------------------------------------

// localVenue is a TEST-ONLY stand-in for the host executor on a `host: local`
// venue: the host side of the reverse channel runs the same script through a
// shell. It is not product code and is never used outside this test.
type localVenue struct{}

func (localVenue) RunCapture(ctx context.Context, script string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), 127, nil
}

// TestVerbLiveLocal runs every method against THIS host's daemon when
// CHARLY_CONTAINERD_LIVE=1, and skips VISIBLY otherwise (the mechanism: the live
// arm is opt-in). Each method logs whether it passed or skipped and why, so the
// run is the receipt.
func TestVerbLiveLocal(t *testing.T) {
	if os.Getenv("CHARLY_CONTAINERD_LIVE") != "1" {
		t.Skip("live containerd probes not requested: set CHARLY_CONTAINERD_LIVE=1 to run against this host's daemon")
	}
	image := os.Getenv("CHARLY_CONTAINERD_LIVE_IMAGE")
	ex := localVenue{}
	ctx := context.Background()

	info, skip, err := dispatchVerb(ctx, ex, &params.ContainerdInput{Method: methodInfo})
	if err != nil || skip != "" {
		t.Fatalf("info live: err=%v skip=%q", err, skip)
	}
	t.Logf("info: %s", info)

	if _, skip, err := dispatchVerb(ctx, ex, &params.ContainerdInput{Method: methodRuntimes}); err != nil {
		t.Fatalf("runtimes live: %v", err)
	} else if skip != "" {
		t.Logf("runtimes SKIP: %s", skip)
	}

	cdi, skip, err := dispatchVerb(ctx, ex, &params.ContainerdInput{Method: methodCDIDevices})
	if err != nil || skip != "" {
		t.Fatalf("cdi_devices live: err=%v skip=%q", err, skip)
	}
	t.Logf("cdi_devices: %s", cdi)

	if _, skip, err := dispatchVerb(ctx, ex, &params.ContainerdInput{Method: methodCRIUPreflight}); err != nil {
		t.Fatalf("criu_preflight live: %v", err)
	} else if skip != "" {
		t.Logf("criu_preflight SKIP: %s", skip)
	}

	if image == "" {
		t.Logf("image SKIP: set CHARLY_CONTAINERD_LIVE_IMAGE to assert an image is present")
		t.Logf("krun_guest SKIP: set CHARLY_CONTAINERD_LIVE_IMAGE to boot a probe guest")
	} else {
		out, skip, err := dispatchVerb(ctx, ex, &params.ContainerdInput{Method: methodImage, Image: image})
		if err != nil || skip != "" {
			t.Fatalf("image live: err=%v skip=%q", err, skip)
		}
		t.Logf("image: %s", out)

		if _, skip, err := dispatchVerb(ctx, ex, &params.ContainerdInput{Method: methodKrunGuest, Image: image}); err != nil {
			t.Fatalf("krun_guest live: %v", err)
		} else if skip != "" {
			t.Logf("krun_guest SKIP: %s", skip)
		}
	}
}
