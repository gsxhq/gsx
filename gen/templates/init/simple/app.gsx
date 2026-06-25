package main

import "github.com/gsxhq/vite"

component Layout(title string) {
	<!DOCTYPE html>
	<html lang="en">
		<head>
			<meta charset="UTF-8" />
			<meta name="viewport" content="width=device-width, initial-scale=1.0" />
			<title>{title}</title>
			{{ assets := vite.FromContext(ctx).Entry("web/main.js") }}
			{ for _, href := range assets.CSS { <link rel="stylesheet" href={href} /> } }
			{ for _, src := range assets.Preloads { <link rel="modulepreload" href={src} /> } }
			{ for _, src := range assets.JS { <script type="module" src={src}></script> } }
		</head>
		<body>{children}</body>
	</html>
}

component Index(title string) {
	<Layout title={title}>
		<div id="app">
			<a href="https://vite.dev" target="_blank" rel="noreferrer"><img src="/public/vite.svg" class="logo" alt="Vite logo" /></a>
			<a href="https://github.com/gsxhq/gsx" target="_blank" rel="noreferrer"><img src="/public/gsx.svg" class="logo gsx" alt="gsx logo" /></a>
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
