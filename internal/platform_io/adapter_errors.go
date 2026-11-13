package platformio

import "errors"

var errNoInboundHandler = errors.New("尚未注册入站回调")
