package storage

// A_memorix 旧存储 pickle 的受限解析器
// 只接受基础数据结构：dict/list/tuple/set/frozenset/str/bytes/int/float/bool/None
// 遇到 GLOBAL/REDUCE/NEWOBJ 等可加载任意对象的操作码立即返回 ErrPickleUnsupported，
// 与 Python _LegacyDataUnpickler.find_class 的拒绝行为一致

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// pickle 操作码，取值来自 CPython Lib/pickle.py
const (
	pickleMark            = '('
	pickleStop            = '.'
	picklePop             = '0'
	picklePopMark         = '1'
	pickleDup             = '2'
	pickleFloat           = 'F'
	pickleInt             = 'I'
	pickleBinInt          = 'J'
	pickleBinInt1         = 'K'
	pickleLong            = 'L'
	pickleBinInt2         = 'M'
	pickleNone            = 'N'
	picklePersID          = 'P'
	pickleBinPersID       = 'Q'
	pickleReduce          = 'R'
	pickleString          = 'S'
	pickleBinString       = 'T'
	pickleShortBinString  = 'U'
	pickleUnicode         = 'V'
	pickleBinUnicode      = 'X'
	pickleAppend          = 'a'
	pickleBuild           = 'b'
	pickleGlobal          = 'c'
	pickleDict            = 'd'
	pickleEmptyDict       = '}'
	pickleAppends         = 'e'
	pickleGet             = 'g'
	pickleBinGet          = 'h'
	pickleInst            = 'i'
	pickleLongBinGet      = 'j'
	pickleList            = 'l'
	pickleEmptyList       = ']'
	pickleObj             = 'o'
	picklePut             = 'p'
	pickleBinPut          = 'q'
	pickleLongBinPut      = 'r'
	pickleSetItem         = 's'
	pickleTuple           = 't'
	pickleEmptyTuple      = ')'
	pickleSetItems        = 'u'
	pickleBinFloat        = 'G'
	pickleProto           = 0x80
	pickleNewObj          = 0x81
	pickleExt1            = 0x82
	pickleExt2            = 0x83
	pickleExt4            = 0x84
	pickleTuple1          = 0x85
	pickleTuple2          = 0x86
	pickleTuple3          = 0x87
	pickleNewTrue         = 0x88
	pickleNewFalse        = 0x89
	pickleLong1           = 0x8a
	pickleLong4           = 0x8b
	pickleShortBinUnicode = 0x8c
	pickleBinUnicode8     = 0x8d
	pickleBinBytes8       = 0x8e
	pickleEmptySet        = 0x8f
	pickleAddItems        = 0x90
	pickleFrozenSet       = 0x91
	pickleNewObjEx        = 0x92
	pickleStackGlobal     = 0x93
	pickleMemoize         = 0x94
	pickleFrame           = 0x95
	pickleByteArray8      = 0x96
	pickleNextBuffer      = 0x97
	pickleReadOnlyBuffer  = 0x98
	pickleBinBytes        = 'B'
	pickleShortBinBytes   = 'C'
)

// pickleTuple 对应 Python tuple，与 list 区分：图元数据要求 nodes 必须是 list
type pickleTupleValue []any

// pickleSet 对应 Python set / frozenset
type pickleSet struct {
	items []any
}

// pickleDict 解析期间的字典，最终转换为 map[string]any
type pickleDictValue struct {
	items map[string]any
}

// pickleList 解析期间的列表，最终转换为 []any
type pickleListValue struct {
	items []any
}

// pickleMark 对应 pickle 的 MARK 标记
type pickleMarkValue struct{}

// pickleDecoder 受限 pickle 解析器状态
type pickleDecoder struct {
	data     []byte
	position int
	stack    []any
	memo     map[uint64]any
	nextMemo uint64
}

// unpickleLegacy 解析旧存储 pickle 载荷，拒绝任何全局对象加载
func unpickleLegacy(payload []byte) (any, error) {
	decoder := &pickleDecoder{data: payload, memo: make(map[uint64]any)}
	value, err := decoder.run()
	if err != nil {
		return nil, err
	}
	return finalizePickleValue(value, 0)
}

// loadLegacyPickle 对齐 _load_legacy_pickle
func loadLegacyPickle(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return unpickleLegacy(data)
}

// loadLegacyPickleDict 对齐 _load_pickle_dict
func loadLegacyPickleDict(path string, label string) (map[string]any, error) {
	value, err := loadLegacyPickle(path)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s必须是 dict: %s", label, path)
	}
	return object, nil
}

// run 逐条执行 pickle 操作码，直到 STOP
func (d *pickleDecoder) run() (any, error) {
	for {
		if d.position >= len(d.data) {
			return nil, fmt.Errorf("旧存储 pickle 数据不完整")
		}
		opcode := d.data[d.position]
		d.position++

		switch opcode {
		case pickleProto:
			if _, err := d.read(1); err != nil {
				return nil, err
			}
		case pickleFrame:
			if _, err := d.read(8); err != nil {
				return nil, err
			}
		case pickleStop:
			return d.pop()
		case pickleMark:
			d.push(pickleMarkValue{})
		case picklePop:
			if _, err := d.pop(); err != nil {
				return nil, err
			}
		case picklePopMark:
			if _, err := d.popToMark(); err != nil {
				return nil, err
			}
		case pickleDup:
			value, err := d.top()
			if err != nil {
				return nil, err
			}
			d.push(value)
		case pickleNone:
			d.push(nil)
		case pickleNewTrue:
			d.push(true)
		case pickleNewFalse:
			d.push(false)
		case pickleInt, pickleLong:
			line, err := d.readLine()
			if err != nil {
				return nil, err
			}
			value, err := parsePickleInteger(string(line))
			if err != nil {
				return nil, err
			}
			d.push(value)
		case pickleFloat:
			line, err := d.readLine()
			if err != nil {
				return nil, err
			}
			value, err := parsePickleFloat(string(line))
			if err != nil {
				return nil, err
			}
			d.push(value)
		case pickleString:
			line, err := d.readLine()
			if err != nil {
				return nil, err
			}
			value, err := unescapePythonStringLiteral(string(line))
			if err != nil {
				return nil, err
			}
			d.push(value)
		case pickleUnicode:
			line, err := d.readLine()
			if err != nil {
				return nil, err
			}
			value, err := unescapePythonUnicodeLiteral(string(line))
			if err != nil {
				return nil, err
			}
			d.push(value)
		case pickleBinString, pickleShortBinString:
			lengthBytes := 4
			if opcode == pickleShortBinString {
				lengthBytes = 1
			}
			raw, err := d.readLengthPrefixed(lengthBytes)
			if err != nil {
				return nil, err
			}
			d.push(raw)
		case pickleBinUnicode, pickleShortBinUnicode, pickleBinUnicode8:
			lengthBytes := 4
			switch opcode {
			case pickleShortBinUnicode:
				lengthBytes = 1
			case pickleBinUnicode8:
				lengthBytes = 8
			}
			text, err := d.readUnicode(lengthBytes)
			if err != nil {
				return nil, err
			}
			d.push(text)
		case pickleBinBytes, pickleShortBinBytes, pickleBinBytes8:
			lengthBytes := 4
			switch opcode {
			case pickleShortBinBytes:
				lengthBytes = 1
			case pickleBinBytes8:
				lengthBytes = 8
			}
			raw, err := d.readLengthPrefixed(lengthBytes)
			if err != nil {
				return nil, err
			}
			d.push(raw)
		case pickleBinInt:
			raw, err := d.read(4)
			if err != nil {
				return nil, err
			}
			d.push(int64(int32(binary.LittleEndian.Uint32(raw))))
		case pickleBinInt1:
			raw, err := d.read(1)
			if err != nil {
				return nil, err
			}
			d.push(int64(raw[0]))
		case pickleBinInt2:
			raw, err := d.read(2)
			if err != nil {
				return nil, err
			}
			d.push(int64(binary.LittleEndian.Uint16(raw)))
		case pickleLong1, pickleLong4:
			lengthBytes := 1
			if opcode == pickleLong4 {
				lengthBytes = 4
			}
			raw, err := d.readLengthPrefixed(lengthBytes)
			if err != nil {
				return nil, err
			}
			value, err := decodePickleLong(raw)
			if err != nil {
				return nil, err
			}
			d.push(value)
		case pickleBinFloat:
			raw, err := d.read(8)
			if err != nil {
				return nil, err
			}
			d.push(math.Float64frombits(binary.BigEndian.Uint64(raw)))
		case pickleEmptyDict:
			d.push(&pickleDictValue{items: make(map[string]any)})
		case pickleEmptyList:
			d.push(&pickleListValue{})
		case pickleEmptyTuple:
			d.push(pickleTupleValue{})
		case pickleEmptySet:
			d.push(&pickleSet{})
		case pickleDict:
			items, err := d.popToMark()
			if err != nil {
				return nil, err
			}
			target := &pickleDictValue{items: make(map[string]any, len(items)/2)}
			if err := setPickleDictItems(target, items); err != nil {
				return nil, err
			}
			d.push(target)
		case pickleList:
			items, err := d.popToMark()
			if err != nil {
				return nil, err
			}
			d.push(&pickleListValue{items: items})
		case pickleTuple:
			items, err := d.popToMark()
			if err != nil {
				return nil, err
			}
			d.push(pickleTupleValue(items))
		case pickleFrozenSet:
			items, err := d.popToMark()
			if err != nil {
				return nil, err
			}
			d.push(pickleSet{items: items})
		case pickleTuple1, pickleTuple2, pickleTuple3:
			items, err := d.popItems(int(opcode-pickleTuple1) + 1)
			if err != nil {
				return nil, err
			}
			d.push(pickleTupleValue(items))
		case pickleAppend:
			value, err := d.pop()
			if err != nil {
				return nil, err
			}
			target, err := d.topList()
			if err != nil {
				return nil, err
			}
			target.items = append(target.items, value)
		case pickleAppends:
			items, err := d.popToMark()
			if err != nil {
				return nil, err
			}
			target, err := d.topList()
			if err != nil {
				return nil, err
			}
			target.items = append(target.items, items...)
		case pickleSetItem:
			value, err := d.pop()
			if err != nil {
				return nil, err
			}
			key, err := d.pop()
			if err != nil {
				return nil, err
			}
			target, err := d.topDict()
			if err != nil {
				return nil, err
			}
			target.items[pythonStr(key)] = value
		case pickleSetItems:
			items, err := d.popToMark()
			if err != nil {
				return nil, err
			}
			target, err := d.topDict()
			if err != nil {
				return nil, err
			}
			if err := setPickleDictItems(target, items); err != nil {
				return nil, err
			}
		case pickleAddItems:
			items, err := d.popToMark()
			if err != nil {
				return nil, err
			}
			target, err := d.topSet()
			if err != nil {
				return nil, err
			}
			target.items = append(target.items, items...)
		case pickleMemoize:
			value, err := d.top()
			if err != nil {
				return nil, err
			}
			d.memo[d.nextMemo] = value
			d.nextMemo++
		case pickleBinPut:
			raw, err := d.read(1)
			if err != nil {
				return nil, err
			}
			if err := d.storeMemo(uint64(raw[0])); err != nil {
				return nil, err
			}
		case pickleLongBinPut:
			raw, err := d.read(4)
			if err != nil {
				return nil, err
			}
			if err := d.storeMemo(uint64(binary.LittleEndian.Uint32(raw))); err != nil {
				return nil, err
			}
		case picklePut:
			line, err := d.readLine()
			if err != nil {
				return nil, err
			}
			index, err := parsePickleInteger(string(line))
			if err != nil {
				return nil, err
			}
			if err := d.storeMemo(uint64(index)); err != nil {
				return nil, err
			}
		case pickleBinGet:
			raw, err := d.read(1)
			if err != nil {
				return nil, err
			}
			if err := d.pushMemo(uint64(raw[0])); err != nil {
				return nil, err
			}
		case pickleLongBinGet:
			raw, err := d.read(4)
			if err != nil {
				return nil, err
			}
			if err := d.pushMemo(uint64(binary.LittleEndian.Uint32(raw))); err != nil {
				return nil, err
			}
		case pickleGet:
			line, err := d.readLine()
			if err != nil {
				return nil, err
			}
			index, err := parsePickleInteger(string(line))
			if err != nil {
				return nil, err
			}
			if err := d.pushMemo(uint64(index)); err != nil {
				return nil, err
			}
		case pickleGlobal:
			module, err := d.readLine()
			if err != nil {
				return nil, err
			}
			name, err := d.readLine()
			if err != nil {
				return nil, err
			}
			return nil, globalObjectError(string(module), string(name))
		case pickleStackGlobal:
			name, err := d.pop()
			if err != nil {
				return nil, err
			}
			module, err := d.pop()
			if err != nil {
				return nil, err
			}
			return nil, globalObjectError(pythonStr(module), pythonStr(name))
		case pickleReduce, pickleBuild, pickleInst, pickleObj, pickleNewObj, pickleNewObjEx,
			pickleExt1, pickleExt2, pickleExt4, picklePersID, pickleBinPersID,
			pickleNextBuffer, pickleReadOnlyBuffer, pickleByteArray8:
			return nil, fmt.Errorf("%w: 旧存储 pickle 不支持操作码 %s", ErrPickleUnsupported, pickleOpcodeName(opcode))
		default:
			return nil, fmt.Errorf("%w: 未识别的 pickle 操作码 0x%02x", ErrPickleUnsupported, opcode)
		}
	}
}
