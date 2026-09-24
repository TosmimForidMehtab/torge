package openapi

import (
	"html/template"
	"strings"
)

// UI selects the interactive documentation renderer served at the docs path.
type UI string

// Supported documentation renderers. Both are loaded from a public CDN; serve
// your own page if the environment forbids third-party scripts.
const (
	SwaggerUI UI = "swagger"
	Scalar    UI = "scalar"
)

var uiTemplates = map[UI]*template.Template{
	SwaggerUI: template.Must(template.New("swagger").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
<script>
window.onload = function () {
  window.ui = SwaggerUIBundle({ url: {{.SpecURL}}, dom_id: "#swagger-ui", deepLinking: true });
};
</script>
</body>
</html>`)),
	Scalar: template.Must(template.New("scalar").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
</head>
<body>
<script id="api-reference" data-url="{{.SpecURL}}"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference" crossorigin></script>
</body>
</html>`)),
}

// DocsHTML renders the documentation page for the given UI.
func DocsHTML(ui UI, title, specURL string) ([]byte, error) {
	tpl, ok := uiTemplates[ui]
	if !ok {
		tpl = uiTemplates[SwaggerUI]
	}
	var b strings.Builder
	err := tpl.Execute(&b, struct{ Title, SpecURL string }{title, specURL})
	return []byte(b.String()), err
}
