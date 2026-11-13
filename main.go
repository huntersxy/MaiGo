// Package main 是 maibot 的进程入口。
// 装配逻辑全部位于 internal/bootstrap（Python 对照：src/main.py + src/runtime.py）。
package main

import "maibot-go/internal/bootstrap"

func main() {
	bootstrap.Run()
}
