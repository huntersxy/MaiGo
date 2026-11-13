package computer

// 来宾代理脚本，原样嵌入并复制到每次沙盒的 C:\MaiBootstrap\guest.ps1

import _ "embed"

//go:embed guest.ps1
var guestScript []byte
