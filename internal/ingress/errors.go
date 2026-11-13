package ingress

// The edge error page: what a visitor sees when Caddy itself answers 502,
// 503 or 504 — no upstream answered, even after try_duration. The app
// cannot serve its own page then (it is the thing that is down), so the
// edge does: a small page that says the app is coming back and reloads
// itself every 5 seconds, with Retry-After for clients that read it.

// edgeErrorPage is the HTML of that page. No external asset: it must work
// when everything behind the edge is down.
const edgeErrorPage = `<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>Voltamos em instantes</title>
<style>
  :root { color-scheme: light dark; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 16px; box-sizing: border-box; font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; background: #f7fafe; color: #0f1b2d; }
  main { max-width: 420px; text-align: center; }
  h1 { margin: 0 0 8px; font-size: 22px; }
  p { margin: 0; color: #5b6b80; }
  .dot { display: inline-block; width: 10px; height: 10px; margin-bottom: 16px; border-radius: 50%; background: #1667d9; animation: pulse 1.2s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: .3; } }
  @media (prefers-color-scheme: dark) { body { background: #0f1420; color: #e8eef7; } p { color: #9aa8bb; } }
</style>
</head>
<body>
<main>
  <span class="dot" aria-hidden="true"></span>
  <h1>Voltamos em instantes</h1>
  <p>Estamos atualizando o sistema. Esta página recarrega sozinha.</p>
</main>
</body>
</html>
`

// errorsConfig is the `errors` block of the HTTP server: 502, 503 and 504
// from the proxy get the edge page, with their own status code.
func errorsConfig() map[string]any {
	return map[string]any{
		"routes": []map[string]any{
			{
				"match": []map[string]any{
					{"expression": "{http.error.status_code} >= 502 && {http.error.status_code} <= 504"},
				},
				"handle": []map[string]any{
					{
						"handler":     "static_response",
						"status_code": "{http.error.status_code}",
						"headers": map[string][]string{
							"Content-Type":  {"text/html; charset=utf-8"},
							"Retry-After":   {"5"},
							"Cache-Control": {"no-store"},
						},
						"body": edgeErrorPage,
					},
				},
			},
		},
	}
}
