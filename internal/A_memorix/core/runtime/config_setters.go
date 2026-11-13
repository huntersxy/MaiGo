package memruntime

// dataclass 风格配置对象的字段赋值器，供 search_runtime_initializer 使用
// 说明：Python 用 SomeConfig(**raw)，Go 版用显式 setter 表复刻未知关键字报错语义

// setterMap 收集字段名到赋值函数的映射
type setterMap map[string]func(any) error

// newSetters 构造空的赋值表
func newSetters() setterMap { return setterMap{} }

// int 注册整型字段
func (m setterMap) int(key string, target *int) setterMap {
	m[key] = intSetter(target)
	return m
}

// float 注册浮点字段
func (m setterMap) float(key string, target *float64) setterMap {
	m[key] = floatSetter(target)
	return m
}

// boolean 注册布尔字段
func (m setterMap) boolean(key string, target *bool) setterMap {
	m[key] = boolSetter(target)
	return m
}

// text 注册字符串字段
func (m setterMap) text(key string, target *string) setterMap {
	m[key] = strSetter(target)
	return m
}

// custom 注册自定义字段
func (m setterMap) custom(key string, setter func(any) error) setterMap {
	m[key] = setter
	return m
}

// apply 按 Python 关键字参数语义给配置对象赋值
func (m setterMap) apply(typeLabel string, raw map[string]any) error {
	return decodeConfigObject(typeLabel, raw, map[string]func(any) error(m))
}
