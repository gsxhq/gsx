# Configuration — `gsx.toml`

A project configures gsx declaratively in a `gsx.toml` file that the **stock
`gsx` binary** reads. This covers the common cases — registering pipeline
filters and attribute-classification rules — with no per-project generator
program. The few options that are Go *functions* (a custom minifier, an
attribute-classifier predicate, a field matcher) still require a code-based
setup; see [Extensions](./extensions.md).

```toml
# gsx.toml — typically at the repo root
[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"
```

With that file in place, `gsx generate` makes `url`/`id`/`target` available as
pipeline filters in any `.gsx` file:

```gsx
<a href={ aboutPage{} |> url }>About</a>
<div id={ MyPage.UserList |> id } hx-target={ MyPage.UserList |> target }>…</div>
```

## Location & discovery

gsx finds `gsx.toml` by walking **up** from the directory it generates in and
using the **first one it finds**:

- The walk is **bounded by the git repo root** (the nearest ancestor containing
  `.git`). A `gsx.toml` above the repo is never read.
- If the directory is not in a git repo, the bound falls back to the **module
  root** (the nearest `go.mod`).
- There is **no global / `$HOME` config** — every key is a Go import path, which
  is project-specific by nature, so the walk never escapes the project.

Because the walk crosses module boundaries (it stops at the repo, not at each
`go.mod`), **one `gsx.toml` at the repo root serves every module in the repo**:

```
myrepo/
  .git/
  gsx.toml          ← discovered by both modules below
  service/  go.mod  pages.gsx
  admin/    go.mod  pages.gsx
```

A nearer `gsx.toml` wins wholesale over an ancestor one (there is no merging
across files), so a single module can override the shared config by dropping its
own `gsx.toml`.

> A config beside `go.mod` is also a stable project-root anchor for editor
> tooling.

## Options

### `[filters]` — named pipeline filters

A table of `name = "<pkgPath>.<Func>"`. Each entry registers one Go function as
a pipeline filter named `name`, usable as `{ value |> name(args…) }`.

```toml
[filters]
url    = "github.com/jackielii/structpages.URLFor"
upper  = "example.com/text.ToUpper"
```

The lowering follows the standard [pipeline rules](./syntax.md): the piped value
becomes the first non-`ctx` argument, `ctx` is injected automatically when the
function's first parameter is `context.Context`, the stage's own arguments
follow, and a `(T, error)` return is auto-unwrapped. So:

```gsx
{ toggle{} |> url("id", todo.ID) }
```

lowers to `structpages.URLFor(ctx, toggle{}, "id", todo.ID)` with the error
propagated out of `Render`.

The function must be an **exported top-level function** in a package the
generated module can import (it is resolved by the Go type-checker against that
module). A non-exported name, a method value, or a missing function is a clear
config error.

### `filterPackages` — bulk filter packages

A list of package import paths. **Every** exported function in each listed
package is registered as a filter, named by its lower-cased function name
(`Upper` → `upper`, `Truncate` → `truncate`). This is the bulk form of
`[filters]`; use it when you want a whole package of helpers available without
naming each one.

```toml
filterPackages = ["example.com/myproject/templatefuncs"]
```

The gsx built-in `std` filter package is **always available** — you do not list
it. It ships `upper`, `lower`, `trim`, `truncate`, `join`, `default`, and
`format` (a `fmt.Sprintf` wrapper with the piped value as the first verb:
`{ price |> format("$%.2f") }`). List `filterPackages` only for your own
packages, or to set precedence (later packages win on name collisions).

### `[[jsAttrs]]` / `[[urlAttrs]]` / `[[cssAttrs]]` — attribute contexts

gsx auto-escapes attribute values by **security context** (JS, URL, CSS, or
plain). The built-ins cover standard HTML, Alpine, and HTMX. If your project
uses a framework with its own event/URL/style attributes, register additional
rules so the escaper — and `@{ }` hole splitting — treat them correctly.

Each rule matches by **exact name** (`name`, case-insensitive) **or by prefix**
(`prefix`) — set exactly one:

```toml
# Livewire wire: attributes carry JS expressions.
[[jsAttrs]]
prefix = "wire:"

# A specific URL-bearing attribute.
[[urlAttrs]]
name = "data-href"

# A specific style-bearing attribute.
[[cssAttrs]]
name = "data-style"
```

Rules are **additive** — they extend the built-ins, never downgrade them. The
built-ins are checked first; your rules apply only to names they did not already
classify. (This is the declarative half of [custom attribute
classification](./extensions.md#custom-attribute-classification); the predicate
escape hatch remains code-only.)

## Full example

```toml
# gsx.toml

# Named pipeline filters: { value |> name(args) }
[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"

# (optional) packages whose exported funcs are all registered as filters,
# named by lower-cased func name. std is always available and not listed.
filterPackages = ["example.com/myproject/templatefuncs"]

# Attribute escaping contexts beyond the built-ins.
[[jsAttrs]]
prefix = "wire:"
[[urlAttrs]]
name = "data-href"
```

## What is *not* in `gsx.toml`

Options whose value is a Go **function** cannot be expressed in TOML and stay
code-only, configured through a project `cmd/gsx/main.go` that calls `gen.Main`
(see [Extensions](./extensions.md)):

- a custom CSS/JS minifier (`gen.WithCSSMinifier` / `gen.WithJSMinifier`),
- an attribute-classifier **predicate** (`gen.WithAttrClassifier`),
- a field matcher (`gen.WithFieldMatcher`).

When a project does use a `cmd/gsx` binary, `gen.Main` loads `gsx.toml` as the
**base** configuration and applies the programmatic options **on top** (filters
and rules from code are appended; a code-registered filter overrides a
same-named config filter). So even a code-configured project keeps its simple
filters/rules in `gsx.toml` and writes Go only for the function-valued options.

> **Unknown keys are rejected.** A typo (e.g. `filterz`, or a misspelled nested
> key) fails generation with an error naming the offending key — gsx does not
> silently ignore unrecognized configuration.

## Verifying with `gsx info`

`gsx info` is the single source of truth for the configuration in effect. It
prints the discovered `gsx.toml` path (or `config: none`) and the fully-resolved
filters and attribute rules — the answer to "which config is active, and why
isn't my `url` filter found":

```sh
gsx info          # human-readable: config path + resolved filters/rules
gsx info --json   # machine-readable (same data)
```

## Generating

The stock binary reads the config — no per-project generator needed:

```sh
go install github.com/gsxhq/gsx/cmd/gsx@latest
gsx generate .            # one package
gsx generate ./a ./b      # several packages (e.g. a multi-package app)
```
