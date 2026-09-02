// Package web 将前端单页应用内嵌到二进制中，运行时无需任何外部静态资源。
package web

import (
	"embed"
	"io/fs"
)

// content 内嵌的静态资源文件系统，包含单页应用页面。
//
//go:embed index.html
var content embed.FS

// FS 返回内嵌资源的可读文件系统。
func FS() fs.FS { return content }

// Index 返回内嵌的首页内容，读取失败时返回空字节。
func Index() []byte {
	data, err := content.ReadFile("index.html")
	if err != nil {
		return nil
	}
	return data
}


