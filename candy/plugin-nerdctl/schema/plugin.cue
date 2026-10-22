// plugin-nerdctl's OWN self-contained CUE schema — the SINGLE SOURCE for this plugin's
// served declaration surface (there is no schema-less plugin: every plugin ships a
// non-empty schema over Describe).
//
// SELF-CONTAINED and PACKAGE-LESS: it references no base def and carries no package
// clause, so it compiles STANDALONE — the property the SDK's serve-side compile needs
// and the property that lets the host splice `base ++ plugin` at the load gate
// (registerPluginUnitSchema); a self-contained schema that will not splice is a LOUD
// load failure.
//
// NO GO CONSUMER: the plugin declares no typed `plugin_input` (its authored input is
// its pass-through CLI grammar), so this schema generates NO `params` package and has
// NO `cue exp gengotypes` artifact — it is the SERVED documentation/config surface,
// not a code-generation source.
//
// It DOCUMENTS the `engine: containerd` word (plus the `verb:` / `command:`
// convenience words). The engine request/reply envelopes live in the base spec schema
// (`spec/schema/engine.cue`), so no authored input is declared here. `nerdctl` is the
// CLI BINARY the containerd word is driven through (#EngineBinaries), not an engine
// word; the retired engine spelling is deleted (R5, no alias).
#ContainerdPlugin: {
	// The engine WORD the plugin serves (the registry key `engine:containerd`).
	engine: "containerd"

	// What the plugin does, in one line (the public-docs surface).
	contract: string & !=""

	// rootful names how this provider implements a rootful start plan: it prefixes
	// the argv with the capability's `privilege_prefix` (e.g. `sudo -n`), taken from
	// the spec table, so a rootful request is never silently run as rootless.
	// `unit_emit` is a pure renderer and its envelope carries no scope field: it
	// renders a user unit from the `start_argv` it is given. A rootful unit carries
	// the prefix and `--runtime` only when the caller passes the rootful start-plan
	// argv; this provider does not re-apply the policy at the unit boundary.
	rootful: "privilege_prefix"
}
