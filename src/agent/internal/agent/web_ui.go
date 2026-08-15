package agent

import (
	"embed"
	"io/fs"
)

//go:embed web_ui
var embeddedWebUI embed.FS

var webUIFiles = mustWebUISubFS()

var webUI = mustReadWebUIFile("index.html")

func mustWebUISubFS() fs.FS {
	files, err := fs.Sub(embeddedWebUI, "web_ui")
	if err != nil {
		panic("open embedded web UI: " + err.Error())
	}
	return files
}

func mustReadWebUIFile(name string) string {
	data, err := fs.ReadFile(webUIFiles, name)
	if err != nil {
		panic("read embedded web UI file " + name + ": " + err.Error())
	}
	return string(data)
}
