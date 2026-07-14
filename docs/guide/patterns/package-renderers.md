# Package renderers

Some database and framework types carry both a value and application policy.
`pgtype.Timestamptz`, for example, can represent SQL `NULL`, finite timestamps,
and positive or negative infinity. A package renderer keeps the policy for those
states in one application-owned function while letting every consumer render the
value directly.

This pattern puts that function in a small `.gsx` package. Returning `gsx.Node`
lets the renderer own semantic markup as well as text formatting.

## The recipe

### 1. Define the renderer in `.gsx`

Create `ds/renderers/timestamptz.gsx`:

```gsx
package renderers

import (
	"time"

	"github.com/gsxhq/gsx"
	"github.com/jackc/pgx/v5/pgtype"
)

func Timestamptz(v pgtype.Timestamptz) gsx.Node {
	if !v.Valid {
		return <></>
	}
	switch v.InfinityModifier {
	case pgtype.Infinity:
		return <time>infinity</time>
	case pgtype.NegativeInfinity:
		return <time>-infinity</time>
	default:
		return <time datetime={v.Time.Format(time.RFC3339)}>{v.Time.Format(time.DateTime)}</time>
	}
}
```

Each branch makes an application decision:

- An invalid value, representing SQL `NULL`, renders no node.
- Positive and negative infinity get explicit labels, without pretending they
  have a finite machine-readable timestamp.
- A finite value uses RFC 3339 for the `<time datetime>` value and
  `time.DateTime` for its visible label.

### 2. Register the type and function

Map the exact pgx type to the renderer's fully qualified function in
`gsx.toml`:

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Timestamptz" = "example.com/app/ds/renderers.Timestamptz"
```

Replace `example.com/app` with your module path. Renderer registrations apply
module-wide, so consumers do not import or call this function themselves.

### 3. Generate the renderer package

Generate the package like any other `.gsx` package:

```bash
go tool gsx generate ./ds/renderers
```

This command works from a clean checkout: because the configured renderer target
is inside the active module, gsx bootstraps its declarations directly from the
local `.gsx` source. It does not require a pre-existing `.x.go` file or a Go
companion file.

That source bootstrap is deliberately module-local. If a configured renderer
target is outside the active module, its package is loaded as an external Go
dependency and must already be buildable — including any generated Go it needs.
Generate and publish that package before consuming it from another module.

### 4. Render the value directly

Consumers write the typed value exactly where it belongs:

```gsx
component AuditRow(row AuditRecord) {
	<tr>
		<td>{row.CreatedAt}</td>
	</tr>
}
```

The registration makes gsx call `renderers.Timestamptz` at that interpolation;
the consumer stays free of validity checks, formatting calls, and wrapper
components.

## Keep policy in the application

The recipe is a starting policy, not a hidden pgx default. Your application owns
what SQL `NULL` means on the page, how both infinity values are presented, which
timezone a finite value uses, the machine-readable `datetime` value, and the
visible label. Change the renderer deliberately when any of those decisions
change; every direct consumer then follows the same policy.

For the complete registration contract — supported signatures, contextual
escaping, and exact type matching — see
[`[renderers]` in the configuration guide](../config.md#renderers-type-directed-value-rendering).
