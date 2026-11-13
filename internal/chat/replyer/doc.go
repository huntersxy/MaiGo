// Package replyer 移植 MaiBot 回复生成与发送前处理。
//
// Python 来源：maisaka_generator_base.py（生成循环、Hook、重试）、maisaka_generator.py、
// document_generator.py（长文）、retro_prompt.py、maisaka_expression_selector.py、
// replyer_manager.py（实例缓存）、chat/utils/utils.py（断句与打字延迟）。
//
// 生成器通过端口解耦，接线前不会自行伪造模型调用。
package replyer
