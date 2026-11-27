from __future__ import annotations

from typing import ClassVar, Literal

from maibot_sdk import Field, PluginConfigBase


class PluginSection(PluginConfigBase):
    __ui_label__: ClassVar[str] = "插件"
    enabled: bool = Field(default=True, description="是否启用插件")
    config_version: str = Field(default="1.7.2", description="配置版本")


class CredentialSection(PluginConfigBase):
    __ui_label__: ClassVar[str] = "凭据"
    sessdata: str = Field(default="", description="Bilibili SESSDATA")
    proxy: str = Field(default="", description="Bilibili API 代理，例如 http://127.0.0.1:7890")
    bangumi_token: str = Field(default="", description="可选 Bangumi Access Token")


class PollingSection(PluginConfigBase):
    __ui_label__: ClassVar[str] = "轮询"
    interval_secs: int = Field(default=300, ge=1, description="单个 UP 主检测周期（秒）")
    task_gap_secs: int = Field(default=20, ge=0, description="相邻 UP 主任务最小间隔（秒）")
    reconnect_silent: bool = Field(default=False, description="启动后静默消化积压")
    dynamic_limit: int = Field(default=5, ge=1, description="单次动态推送数量限制")
    recent_dynamic_cache: int = Field(default=4, ge=1, description="动态缓存大小")


class RenderSection(PluginConfigBase):
    __ui_label__: ClassVar[str] = "渲染"
    rai: bool = Field(default=True, description="是否以图片形式渲染动态与直播推送")
    renderer_template: Literal["template_1", "template_2", "simple"] = Field(
        default="template_2", description="渲染模板"
    )
    node: bool = Field(default=False, description="是否以转发消息形式推送")
    plain_push_template: str = Field(default="", description="纯文本推送模板")
    plain_push_forward_template: str = Field(default="", description="纯文本转发模板")


class ParseSection(PluginConfigBase):
    __ui_label__: ClassVar[str] = "解析"
    enable_parse_miniapp: bool = Field(default=True, description="是否解析小程序")
    enable_parse_BV: bool = Field(default=True, description="是否解析 B 站视频链接")


class AISection(PluginConfigBase):
    __ui_label__: ClassVar[str] = "AI 摘要"
    enable_ai_summary: bool = Field(default=False, description="动态推送后额外生成 AI 摘要")
    ai_summary_prompt: str = Field(default="", description="AI 摘要提示词，可用 {content}")


class BilibiliPluginConfig(PluginConfigBase):
    plugin: PluginSection = Field(default_factory=PluginSection)
    credential: CredentialSection = Field(default_factory=CredentialSection)
    polling: PollingSection = Field(default_factory=PollingSection)
    render: RenderSection = Field(default_factory=RenderSection)
    parse: ParseSection = Field(default_factory=ParseSection)
    ai: AISection = Field(default_factory=AISection)
