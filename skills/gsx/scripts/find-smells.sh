#!/usr/bin/env bash
# List candidate sites of common non-idiomatic gsx, grouped by the rewrite
# table in ../SKILL.md. Grep-level: every hit is a candidate to read, not a
# verdict. Usage: bash find-smells.sh [dir]   (default: .)
set -u
dir="${1:-.}"
S='[[:space:]]'

# section TITLE PATTERN [grep flags…]; set SKIP to drop hits matching a regex.
section() {
	local title="$1" pattern="$2"
	shift 2
	local hits
	hits=$(grep -rnE "$@" "${skipdirs[@]}" -- "$pattern" "$dir" 2>/dev/null)
	if [ -n "${SKIP:-}" ] && [ -n "$hits" ]; then
		hits=$(printf '%s\n' "$hits" | grep -vE -- "$SKIP")
	fi
	[ -z "$hits" ] && return
	printf '\n== %s (%d)\n%s\n' "$title" "$(printf '%s\n' "$hits" | wc -l | tr -d ' ')" "$hits"
}

skipdirs=(--exclude-dir=node_modules --exclude-dir=.git --exclude-dir=.claude --exclude-dir=.worktrees --exclude-dir=vendor)
gsx=(--include=*.gsx)
gsxgo=(--include=*.gsx --include=*.go --exclude=*.x.go)

# Security first: string-built JS is only HTML-escaped.
section "JS attribute built from strings (use js\`…@{v}…\`)" \
	"(^|$S)(on[a-z]+|@[A-Za-z.:-]+|x-[a-z:.-]+|:[a-z-]+|hx-(on[^=]*|vals|vars))=\{[^}]*(\"$S*\+|\+$S*\"|Sprintf)" "${gsx[@]}"
section "gsx.RawJS / RawCSS / RawURL / Raw (justify each, or use a literal)" \
	'gsx\.Raw(JS|CSS|URL)?\(' "${gsxgo[@]}"
section "style built from strings (ZgotmplZ risk; use css\`…@{v}…\`)" \
	"style=\{[^}]*(\+|Sprintf|strconv)" "${gsx[@]}"
section "hand-built JSON for attributes (use js\`{\"k\": @{v}}\`)" \
	'json\.Marshal' "${gsxgo[@]}"

section "string-built attribute value (use f\`…@{v}…\`)" \
	"[A-Za-z-]+=\{$S*(fmt\.Sprint|\"[^\"]*\"$S*\+)" "${gsx[@]}"
section "class chosen in a Go helper (use class={ …, switch/if, \"cls\": cond })" \
	'func [A-Za-z_]*[Cc]lass(es|Name)?\(' "${gsxgo[@]}"
section "string concatenation inside class" \
	"class=\{[^}]*(\"$S*\+|\+$S*\")" "${gsx[@]}"
section "bool stringified for aria-* (aria-x={b} already renders \"true\"/\"false\")" \
	"aria-[a-z]+=\{$S*(strconv\.FormatBool|fmt\.Sprint\()" "${gsx[@]}"
section "value formatted in Go before interpolating (interpolate the value, or a filter)" \
	"\{$S*(fmt\.Sprintf?\(\"%[dvs]\"|strconv\.(Itoa|FormatInt|FormatFloat)|cmp\.Or)" "${gsx[@]}"
# Only callees declared with `component` under $dir; other calls (a message
# constructor, a Node-returning helper) are values, not component calls.
components=$(grep -rhoE "${skipdirs[@]}" "${gsx[@]}" -- "^component$S+(\([^)]*\)$S*)?[A-Z][A-Za-z0-9_]*" "$dir" 2>/dev/null |
	sed -E 's/.*[[:space:])]//' | sort -u | paste -sd '|' -)
[ -n "$components" ] && SKIP='\{[[:space:]]*gsx\.' section "component called as a Go function (use <Tag attr={…}/>)" \
	"^$S*\{$S*([a-z_][a-z0-9_]*\.)?($components)(\[[^]]*\])?\([^)]*\)$S*\}$S*$" "${gsx[@]}"
section "comment shipped to the browser or hidden in a Go block (use // or {/* */})" \
	"gsx\.Raw\(\"<!--|\{\{$S*/[*/]" "${gsx[@]}"
