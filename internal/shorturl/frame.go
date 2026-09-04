package shorturl

import (
	"html/template"
	"io"
)

// frameTemplate renders a destination inside a full-page iframe. The script
// rewrites the address bar back to the short URL so visitors see the short
// link rather than the destination. html/template escapes the title in text
// and attribute context and JSON-encodes the URL inside the script.
var frameTemplate = template.Must(template.New("frame").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<script>window.history.replaceState(null,"",{{.ShortURL}})</script>
</head><body style="padding:0;margin:0;width:100%;height:100%">
<iframe style="border:0;width:100%;height:100%" title="{{.Title}}" src="{{.Destination}}"></iframe>
</body></html>
`))

// framePage is the data for frameTemplate.
type framePage struct {
	Title       string
	ShortURL    string
	Destination string
}

// writeFrame renders the frame page to w.
func writeFrame(w io.Writer, title, shortURL, destination string) error {
	return frameTemplate.Execute(w, framePage{Title: title, ShortURL: shortURL, Destination: destination})
}
