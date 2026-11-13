package storage

// pickle 协议 0 文本字面量的解析辅助
// 对应 CPython pickle 对 INT/FLOAT/STRING/UNICODE/LONG 操作码的文本解码

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// parsePickleInteger 解析协议 0 的整数文本，对齐 Python int(text)
func parsePickleInteger(text string) (int64, error) {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return 0, fmt.Errorf("旧存储 pickle 整数文本为空")
	}
	cleaned = strings.ReplaceAll(cleaned, "_", "")
	value, err := strconv.ParseInt(cleaned, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("旧存储 pickle 整数格式非法: %q", text)
	}
	return value, nil
}

// parsePickleFloat 解析协议 0 的浮点文本，对齐 Python float(text)
func parsePickleFloat(text string) (float64, error) {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return 0, fmt.Errorf("旧存储 pickle 浮点文本为空")
	}
	cleaned = strings.ReplaceAll(cleaned, "_", "")
	value, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, fmt.Errorf("旧存储 pickle 浮点格式非法: %q", text)
	}
	return value, nil
}

// decodePickleLong 解析 LONG1/LONG4 的小端二进制补码整数
// Python 侧为任意精度，这里限制在 int64 范围内，超出即报错而不是截断
func decodePickleLong(raw []byte) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	if len(raw) > 8 {
		negative := raw[len(raw)-1]&0x80 != 0
		fits := true
		for _, item := range raw[:len(raw)-8] {
			if negative && item != 0xff {
				fits = false
				break
			}
			if !negative && item != 0x00 {
				fits = false
				break
			}
		}
		if !fits {
			return 0, fmt.Errorf("旧存储 pickle 整数超出 int64 范围")
		}
		raw = raw[len(raw)-8:]
	}
	littleEndian := make([]byte, len(raw))
	for index := range raw {
		littleEndian[index] = raw[len(raw)-1-index]
	}
	var value int64
	for _, item := range littleEndian {
		value = value<<8 | int64(item)
	}
	if len(raw) < 8 && raw[len(raw)-1]&0x80 != 0 {
		value |= -1 << (uint(len(raw)) * 8)
	}
	return value, nil
}

// unescapePythonStringLiteral 解析协议 0 STRING 的字面量文本
// pickle 写入的是转义后的 UTF-8 字节串，可能带引号
func unescapePythonStringLiteral(text string) (string, error) {
	body := pythonStringBody(text)
	raw, err := unescapePythonEscapes(body, false)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("旧存储 pickle 字符串不是合法 UTF-8")
	}
	return string(raw), nil
}

// unescapePythonUnicodeLiteral 解析协议 0 UNICODE 的 raw-unicode-escape 文本
func unescapePythonUnicodeLiteral(text string) (string, error) {
	body := pythonStringBody(text)
	var builder strings.Builder
	for index := 0; index < len(body); index++ {
		if body[index] != '\\' {
			builder.WriteRune(rune(body[index]))
			continue
		}
		escaped, next, err := decodeUnicodeEscape(body, index)
		if err != nil {
			return "", err
		}
		builder.WriteRune(escaped)
		index = next
	}
	return builder.String(), nil
}

// decodeUnicodeEscape 解析 index 处的反斜杠转义，返回字符与下一个位置
func decodeUnicodeEscape(body string, index int) (rune, int, error) {
	rest := body[index:]
	switch {
	case strings.HasPrefix(rest, "\\u") && len(rest) >= 6:
		value, err := strconv.ParseUint(rest[2:6], 16, 32)
		if err != nil {
			return 0, index, fmt.Errorf("旧存储 pickle 的 \\u 转义非法: %q", rest[:6])
		}
		return rune(value), index + 5, nil
	case strings.HasPrefix(rest, "\\U") && len(rest) >= 10:
		value, err := strconv.ParseUint(rest[2:10], 16, 32)
		if err != nil {
			return 0, index, fmt.Errorf("旧存储 pickle 的 \\U 转义非法: %q", rest[:10])
		}
		return rune(value), index + 9, nil
	}
	escaped, consumed, err := decodeSimpleEscape(body, index)
	if err != nil {
		return 0, index, err
	}
	return escaped, index + consumed - 1, nil
}

// pythonStringBody 去掉字面量两侧的引号
func pythonStringBody(text string) string {
	if len(text) >= 2 {
		head := text[0]
		tail := text[len(text)-1]
		if (head == '\'' && tail == '\'') || (head == '"' && tail == '"') {
			return text[1 : len(text)-1]
		}
	}
	return text
}

// unescapePythonEscapes 解析 Python 字节串转义序列
func unescapePythonEscapes(body string, unicodeMode bool) ([]byte, error) {
	output := make([]byte, 0, len(body))
	for index := 0; index < len(body); index++ {
		if body[index] != '\\' {
			output = append(output, body[index])
			continue
		}
		if index+1 >= len(body) {
			return nil, fmt.Errorf("旧存储 pickle 的转义序列不完整")
		}
		escaped, consumed, err := decodeSimpleEscape(body, index)
		if err != nil {
			return nil, err
		}
		index += consumed - 1
		output = append(output, []byte(string(escaped))...)
	}
	return output, nil
}

// decodeSimpleEscape 解析单字符、八进制与 \\x 转义，返回字符和占用长度
func decodeSimpleEscape(body string, index int) (rune, int, error) {
	if index+1 >= len(body) {
		return 0, 0, fmt.Errorf("旧存储 pickle 的转义序列不完整")
	}
	next := body[index+1]
	switch next {
	case '\\':
		return '\\', 2, nil
	case '\'':
		return '\'', 2, nil
	case '"':
		return '"', 2, nil
	case 'n':
		return '\n', 2, nil
	case 'r':
		return '\r', 2, nil
	case 't':
		return '\t', 2, nil
	case 'a':
		return 7, 2, nil
	case 'b':
		return 8, 2, nil
	case 'f':
		return 12, 2, nil
	case 'v':
		return 11, 2, nil
	case 'x':
		if index+3 >= len(body) {
			return 0, 0, fmt.Errorf("旧存储 pickle 的 \\x 转义不完整: %q", body[index:])
		}
		value, err := parseHexValue(body[index+2 : index+4])
		if err != nil {
			return 0, 0, err
		}
		return rune(value), 4, nil
	}
	if next >= '0' && next <= '7' {
		digits := 1
		for digits < 3 && index+1+digits < len(body) {
			digit := body[index+1+digits]
			if digit < '0' || digit > '7' {
				break
			}
			digits++
		}
		value, err := strconv.ParseUint(body[index+1:index+1+digits], 8, 32)
		if err != nil {
			return 0, 0, fmt.Errorf("旧存储 pickle 的八进制转义非法: %q", body[index:index+1+digits])
		}
		return rune(value), digits + 1, nil
	}
	return rune(next), 2, nil
}

// parseHexValue 解析两位十六进制
func parseHexValue(text string) (byte, error) {
	if len(text) != 2 {
		return 0, fmt.Errorf("旧存储 pickle 的十六进制转义非法: %q", text)
	}
	high, err := parseHexByte(text[0])
	if err != nil {
		return 0, err
	}
	low, err := parseHexByte(text[1])
	if err != nil {
		return 0, err
	}
	return high<<4 | low, nil
}

// parseHexByte 解析单个十六进制字符
func parseHexByte(value byte) (byte, error) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', nil
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, nil
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, nil
	}
	return 0, fmt.Errorf("旧存储 pickle 的十六进制字符非法: %q", string(value))
}

// globalObjectError 构造拒绝加载全局对象的错误
func globalObjectError(module string, name string) error {
	return fmt.Errorf("%w: 旧存储 pickle 禁止加载全局对象: %s.%s", ErrPickleUnsupported, module, name)
}

// pickleOpcodeName 返回操作码的可读名称
func pickleOpcodeName(opcode byte) string {
	names := map[byte]string{
		pickleMark: "MARK", pickleStop: "STOP", picklePop: "POP", picklePopMark: "POP_MARK",
		pickleDup: "DUP", pickleFloat: "FLOAT", pickleInt: "INT", pickleBinInt: "BININT",
		pickleBinInt1: "BININT1", pickleLong: "LONG", pickleBinInt2: "BININT2",
		pickleNone: "NONE", picklePersID: "PERSID", pickleBinPersID: "BINPERSID",
		pickleReduce: "REDUCE", pickleString: "STRING", pickleBinString: "BINSTRING",
		pickleShortBinString: "SHORT_BINSTRING", pickleUnicode: "UNICODE",
		pickleBinUnicode: "BINUNICODE", pickleAppend: "APPEND", pickleBuild: "BUILD",
		pickleGlobal: "GLOBAL", pickleDict: "DICT", pickleEmptyDict: "EMPTY_DICT",
		pickleAppends: "APPENDS", pickleGet: "GET", pickleBinGet: "BINGET",
		pickleInst: "INST", pickleLongBinGet: "LONG_BINGET", pickleList: "LIST",
		pickleEmptyList: "EMPTY_LIST", pickleObj: "OBJ", picklePut: "PUT",
		pickleBinPut: "BINPUT", pickleLongBinPut: "LONG_BINPUT", pickleSetItem: "SETITEM",
		pickleTuple: "TUPLE", pickleEmptyTuple: "EMPTY_TUPLE", pickleSetItems: "SETITEMS",
		pickleBinFloat: "BINFLOAT", pickleProto: "PROTO", pickleNewObj: "NEWOBJ",
		pickleExt1: "EXT1", pickleExt2: "EXT2", pickleExt4: "EXT4",
		pickleTuple1: "TUPLE1", pickleTuple2: "TUPLE2", pickleTuple3: "TUPLE3",
		pickleNewTrue: "NEWTRUE", pickleNewFalse: "NEWFALSE", pickleLong1: "LONG1",
		pickleLong4: "LONG4", pickleShortBinUnicode: "SHORT_BINUNICODE",
		pickleBinUnicode8: "BINUNICODE8", pickleBinBytes8: "BINBYTES8",
		pickleEmptySet: "EMPTY_SET", pickleAddItems: "ADDITEMS", pickleFrozenSet: "FROZENSET",
		pickleNewObjEx: "NEWOBJ_EX", pickleStackGlobal: "STACK_GLOBAL",
		pickleMemoize: "MEMOIZE", pickleFrame: "FRAME", pickleByteArray8: "BYTEARRAY8",
		pickleNextBuffer: "NEXT_BUFFER", pickleReadOnlyBuffer: "READONLY_BUFFER",
		pickleBinBytes: "BINBYTES", pickleShortBinBytes: "SHORT_BINBYTES",
	}
	if name, ok := names[opcode]; ok {
		return name
	}
	return fmt.Sprintf("0x%02x", opcode)
}
