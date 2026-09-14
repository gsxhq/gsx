# Translation keys

A translation key is one value, but the page needs the string for the
request's locale. A [type renderer](../config.md#renderers-type-directed-value-rendering)
turns that into one registration: write the key in markup and the translation
renders. Consumers never call a translate function.

## The recipe

### 1. Define the key type and its renderer

Create `i18n/i18n.go`:

```go
package i18n

import "context"

// Key is a translation key. Writing one in markup renders its translation.
type Key string

// Translator resolves keys for one request's locale.
type Translator interface {
	Lookup(key string) (string, bool)
}

type ctxKey struct{}

func WithTranslator(ctx context.Context, t Translator) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// Translate is the renderer registered for Key. A missing translator or key
// renders the key itself, so an untranslated page stays legible.
func Translate(ctx context.Context, k Key) string {
	if t, ok := ctx.Value(ctxKey{}).(Translator); ok {
		if s, ok := t.Lookup(string(k)); ok {
			return s
		}
	}
	return string(k)
}
```

The renderer takes `context.Context` first. gsx passes the render context to
it, so the translator travels with the request and no component threads a
locale parameter.

### 2. Register the type

```toml
[renderers]
"example.com/app/i18n.Key" = "example.com/app/i18n.Translate"
```

Replace `example.com/app` with your module path.

### 3. Name the keys

Create `msg/msg.go`:

```go
package msg

import "example.com/app/i18n"

const (
	Save     i18n.Key = "action.save"
	SaveHint i18n.Key = "action.save.hint"
)
```

Typed constants keep keys greppable and make a misspelled key a compile error.
A literal `i18n.Key("greeting")` renders the same way.

### 4. Write the keys in markup

```gsx
component Toolbar() {
	<button title={msg.SaveHint}>{msg.Save}</button>
}
```

For a German request this renders
`<button title="Speichern">Speichern</button>`. The translation is escaped for
the position it lands in, text or attribute, like any other rendered value.

### 5. Put the translator in the request context

```go
func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := i18n.WithTranslator(r.Context(), h.locales.For(r))
	_ = views.Page().Render(ctx, w)
}
```

## Returning markup

A renderer may return `gsx.Node` instead of a string, so it can own markup. A
`.gsx` renderer in the same package can make missing translations visible
during review:

```gsx
package i18n

import (
	"context"

	"github.com/gsxhq/gsx"
)

// Rich is a translation key for content positions.
type Rich string

func Label(ctx context.Context, k Rich) gsx.Node {
	if t, ok := ctx.Value(ctxKey{}).(Translator); ok {
		if s, ok := t.Lookup(string(k)); ok {
			return <>{s}</>
		}
	}
	return <span class="i18n-missing" data-key={string(k)}>{string(k)}</span>
}
```

```toml
[renderers]
"example.com/app/i18n.Key" = "example.com/app/i18n.Translate"
"example.com/app/i18n.Rich" = "example.com/app/i18n.Label"
```

`{i18n.Rich("greeting")}` renders `Hallo` for a German request and
`<span class="i18n-missing" data-key="greeting">greeting</span>` when the key is
untranslated. A `gsx.Node` cannot render into an attribute, and a type has one
renderer, so `Rich` is a second type: `Key` keeps serving `title`, `aria-label`
and other attributes.

## Messages with arguments

A `Key` carries no arguments. For messages such as "3 items selected",
register a second type, for example `type Msg struct { Key Key; Args []any }`,
with a renderer that looks the key up and formats it. Value and pointer
registrations are separate, so register the shape you write in markup.
