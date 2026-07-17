package main

import (
	"github.com/gsxhq/gsx"
	"github.com/gsxhq/vite"
)

component Layout(title string, children gsx.Node) {
	<!DOCTYPE html>
	<html lang="en">
		<head>
			<meta charset="UTF-8"/>
			<meta name="viewport" content="width=device-width, initial-scale=1.0"/>
			<title>{ title }</title>
			{{ v := vite.FromContext(ctx) }}
			{ if v.Dev() {
				<style>
					html[data-loading] body {
						visibility: hidden;
					}

					html[data-loading] * {
						transition: none !important;
					}
				</style>
				<script>
					// Dev-only FOUC gate. Vite injects CSS through JS a tick after the
					// HTML loads, so hide the page until the entry module (which imports
					// the CSS) has run, then reveal. Prod ships real <link rel=stylesheet>
					// tags below, so no gate is emitted there.
					document.documentElement.dataset.loading = "true";
					window.__gsxReveal = function () {
						// Force a style flush BEFORE dropping the gate, so the
						// unstyled->styled commit lands under "transition: none" and
						// nothing animates.
						void document.documentElement.offsetHeight;
						document.documentElement.removeAttribute("data-loading");
					};
					// Safety net: reveal anyway if the entry module never loads.
					setTimeout(window.__gsxReveal, 5000);
				</script>
			} }
			{{ assets := v.Entry("web/main.js") }}
			{ for _, href := range assets.CSS {
				<link rel="stylesheet" href={href}/>
			} }
			{ for _, src := range assets.Preloads {
				<link rel="modulepreload" href={src}/>
			} }
			{ for _, src := range assets.JS {
				<script
					type="module"
					src={src}
					{ if v.Dev() && src == assets.JS[len(assets.JS)-1] {
						onload="window.__gsxReveal()"
					} }
				></script>
			} }
		</head>
		<body>{ children }</body>
	</html>
}

component Index(title string) {
	<Layout title={title}>
		<div id="app">
			<a href="https://vite.dev" target="_blank" rel="noreferrer">
				<img src="/public/vite.svg" class="logo" alt="Vite logo"/>
			</a>
			<a href="https://github.com/gsxhq/gsx" target="_blank" rel="noreferrer">
				<img src="/public/gsx.svg" class="logo gsx" alt="gsx logo"/>
			</a>
			<h1>gsx + Vite</h1>
			<div class="card">
				<button id="counter" type="button">count is 0</button>
			</div>
			<p class="read-the-docs">
				Edit <code>app.gsx</code> and save — the page live-reloads.
			</p>
		</div>
	</Layout>
}
