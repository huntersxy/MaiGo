package storage

// pickle 解析器的栈、memo 与容器收尾辅助
// 与 format_migration_pickle.go 的主循环拆分，保持单文件职责清晰

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

// push 入栈
func (d *pickleDecoder) push(value any) {
	d.stack = append(d.stack, value)
}

// read 读取固定长度字节
func (d *pickleDecoder) read(count int) ([]byte, error) {
	if count < 0 || d.position+count > len(d.data) {
		return nil, fmt.Errorf("旧存储 pickle 数据不完整")
	}
	raw := d.data[d.position : d.position+count]
	d.position += count
	return raw, nil
}

// readLine 读取以换行结尾的行，去掉换行符
func (d *pickleDecoder) readLine() ([]byte, error) {
	for index := d.position; index < len(d.data); index++ {
		if d.data[index] == '\n' {
			line := d.data[d.position:index]
			d.position = index + 1
			return line, nil
		}
	}
	return nil, fmt.Errorf("旧存储 pickle 数据不完整")
}

// readLengthPrefixed 读取 lengthBytes 字节长度前缀加数据
func (d *pickleDecoder) readLengthPrefixed(lengthBytes int) ([]byte, error) {
	raw, err := d.read(lengthBytes)
	if err != nil {
		return nil, err
	}
	var length uint64
	switch lengthBytes {
	case 1:
		length = uint64(raw[0])
	case 4:
		length = uint64(binary.LittleEndian.Uint32(raw))
	case 8:
		length = binary.LittleEndian.Uint64(raw)
	default:
		return nil, fmt.Errorf("旧存储 pickle 长度前缀非法")
	}
	if length > uint64(len(d.data)) {
		return nil, fmt.Errorf("旧存储 pickle 数据不完整")
	}
	return d.read(int(length))
}

// readUnicode 读取 UTF-8 字符串
func (d *pickleDecoder) readUnicode(lengthBytes int) (string, error) {
	raw, err := d.readLengthPrefixed(lengthBytes)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("旧存储 pickle 字符串不是合法 UTF-8")
	}
	return string(raw), nil
}

// pop 出栈
func (d *pickleDecoder) pop() (any, error) {
	if len(d.stack) == 0 {
		return nil, fmt.Errorf("旧存储 pickle 数据不完整")
	}
	value := d.stack[len(d.stack)-1]
	d.stack = d.stack[:len(d.stack)-1]
	return value, nil
}

// top 读取栈顶
func (d *pickleDecoder) top() (any, error) {
	if len(d.stack) == 0 {
		return nil, fmt.Errorf("旧存储 pickle 数据不完整")
	}
	return d.stack[len(d.stack)-1], nil
}

// popItems 依次弹出 count 个元素并按原顺序返回
func (d *pickleDecoder) popItems(count int) ([]any, error) {
	if len(d.stack) < count {
		return nil, fmt.Errorf("旧存储 pickle 数据不完整")
	}
	items := make([]any, count)
	for index := count - 1; index >= 0; index-- {
		value, err := d.pop()
		if err != nil {
			return nil, err
		}
		items[index] = value
	}
	return items, nil
}

// popToMark 弹出最近的 MARK 之上的元素
func (d *pickleDecoder) popToMark() ([]any, error) {
	for index := len(d.stack) - 1; index >= 0; index-- {
		if _, ok := d.stack[index].(pickleMarkValue); ok {
			items := append([]any(nil), d.stack[index+1:]...)
			d.stack = d.stack[:index]
			return items, nil
		}
	}
	return nil, fmt.Errorf("旧存储 pickle 缺少 MARK 标记")
}

// topList 读取栈顶列表
func (d *pickleDecoder) topList() (*pickleListValue, error) {
	value, err := d.top()
	if err != nil {
		return nil, err
	}
	list, ok := value.(*pickleListValue)
	if !ok {
		return nil, fmt.Errorf("旧存储 pickle 的 APPEND 目标不是 list")
	}
	return list, nil
}

// topDict 读取栈顶字典
func (d *pickleDecoder) topDict() (*pickleDictValue, error) {
	value, err := d.top()
	if err != nil {
		return nil, err
	}
	dict, ok := value.(*pickleDictValue)
	if !ok {
		return nil, fmt.Errorf("旧存储 pickle 的 SETITEM 目标不是 dict")
	}
	return dict, nil
}

// topSet 读取栈顶集合
func (d *pickleDecoder) topSet() (*pickleSet, error) {
	value, err := d.top()
	if err != nil {
		return nil, err
	}
	set, ok := value.(*pickleSet)
	if !ok {
		return nil, fmt.Errorf("旧存储 pickle 的 ADDITEMS 目标不是 set")
	}
	return set, nil
}

// storeMemo 记录 memo 索引
func (d *pickleDecoder) storeMemo(index uint64) error {
	value, err := d.top()
	if err != nil {
		return err
	}
	d.memo[index] = value
	if index >= d.nextMemo {
		d.nextMemo = index + 1
	}
	return nil
}

// pushMemo 取出 memo 索引并压栈
func (d *pickleDecoder) pushMemo(index uint64) error {
	value, ok := d.memo[index]
	if !ok {
		return fmt.Errorf("旧存储 pickle 引用了不存在的 memo 索引 %d", index)
	}
	d.push(value)
	return nil
}

// setPickleDictItems 按 key/value 交替写入字典
func setPickleDictItems(target *pickleDictValue, items []any) error {
	if len(items)%2 != 0 {
		return fmt.Errorf("旧存储 pickle 的字典元素个数为奇数")
	}
	for index := 0; index < len(items); index += 2 {
		target.items[pythonStr(items[index])] = items[index+1]
	}
	return nil
}

// finalizePickleValue 把解析期间的容器转换为对外的 Go 值
func finalizePickleValue(value any, depth int) (any, error) {
	if depth > 200 {
		return nil, fmt.Errorf("%w: 旧存储 pickle 嵌套层级过深", ErrPickleUnsupported)
	}
	switch typed := value.(type) {
	case *pickleDictValue:
		object := make(map[string]any, len(typed.items))
		for key, item := range typed.items {
			converted, err := finalizePickleValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = converted
		}
		return object, nil
	case *pickleListValue:
		items := make([]any, len(typed.items))
		for index, item := range typed.items {
			converted, err := finalizePickleValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			items[index] = converted
		}
		return items, nil
	case pickleTupleValue:
		items := make([]any, len(typed))
		for index, item := range typed {
			converted, err := finalizePickleValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			items[index] = converted
		}
		return pickleTupleValue(items), nil
	case *pickleSet:
		items := make([]any, len(typed.items))
		for index, item := range typed.items {
			converted, err := finalizePickleValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			items[index] = converted
		}
		return pickleSet{items: items}, nil
	}
	return value, nil
}
