package config

import "sync/atomic"

var personaSource atomic.Pointer[func() PersonaConfig]

// SetPersonaSource 注册动态人格来源；Reload 时读取，nil 表示使用代码默认值
func SetPersonaSource(source func() PersonaConfig) {
	if source == nil {
		personaSource.Store(nil)
		return
	}
	copied := source
	personaSource.Store(&copied)
}

func currentPersonaConfig() (PersonaConfig, bool) {
	source := personaSource.Load()
	if source == nil {
		return PersonaConfig{}, false
	}
	return (*source)(), true
}
