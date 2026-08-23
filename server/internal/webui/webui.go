// Package webui 内嵌 React 前端构建产物（web/dist 构建后复制到本包 dist/）。
//
// 保持单一二进制发布：`make build` 会先 `npm run build` 并复制到此处。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS 返回解包后的前端 dist 目录（去掉 dist/ 前缀）。
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
