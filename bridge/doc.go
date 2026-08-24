// Package bridge wraps foreign tool surfaces — local command-line programs,
// remote MCP servers, pasted tools/list payloads — as xyz command entries.
// One declarative config (xyz.json) then yields the same command on all
// three xyz-go fronts: CLI subcommands, HTTP routes and MCP tools.
//
// The core primitive is NewTool: a JSON Schema subset plus an Invoker
// becomes a *spec.Entry the xyz-go frontends consume directly, with no
// compile-time struct and no reflection beyond what the frontends read
// from the field metadata. Everything else in this package (exec adapters,
// MCP proxies, config loading) produces schemas and invokers for it.
package bridge
