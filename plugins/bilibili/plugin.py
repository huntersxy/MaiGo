"""Bilibili plugin for MaiBot / MaiGo."""

from __future__ import annotations

import asyncio
import base64
import json
import re
import tempfile
import time
from pathlib import Path
from typing import Any, ClassVar, Dict, List, Optional

from jinja2 import Template
from maibot_sdk import API, Command, EventHandler, MaiBotPlugin, Tool
from maibot_sdk.types import EventType, ToolParameterInfo, ToolParamType

from .bili_client import BiliClient
from .core.bili_login import BiliLoginError, BiliQrLoginClient
from .core.constant import (
    AT_ALL_OPTION,
    AT_SUB_OPTION,
    BV,
    CARD_TEMPLATES,
    CREDENTIAL_MAINTENANCE_INTERVAL_SECS,
    DEFAULT_TEMPLATE,
    LIVE_ATALL_OPTION,
    LOGO_PATH,
    RECONNECT_SILENT_PADDING_SECS,
    RECONNECT_SILENT_THRESHOLD_SECS,
    UNAT_SUB_OPTION,
    VALID_FILTER_TYPES,
    VALID_SUB_OPTIONS,
    get_template_names,
)
from .core.credential_lifecycle import CredentialLifecycle, CredentialRefreshStatus
from .core.data_manager import DataManager
from .core.models import RenderPayload, SubscriptionRecord
from .core.utils import image_to_base64, parse_bilibili_uid
from .services.dispatcher import SubscriptionNotification, SubscriptionNotificationDispatcher
from .services.listener import DynamicListener
from .services.logo_service import LogoService
from .services.message_segments import Image
from .services.renderer import Renderer
from .services.subscription_page_service import SubscriptionPageService
from .services.subscription_service import SubscriptionService
from .settings import BilibiliPluginConfig
from .tools.bgm_daily import BgmDailyTool
from .tools.bgm_subject import BgmAdvancedSubjectSearchTool, BgmRecommendHotSubjectsTool
from .tools.bili_hot_video import BiliSearchHotVideosTool
from .tools.bili_user_dynamics import BiliUserDynamicsTool

CARD_STYLE_ALIAS = "卡片样式"
SUB_ALIAS = "订阅动态"
SUB_LIST_ALIAS = "订阅列表"
SUB_DEL_ALIAS = "订阅删除"
GLOBAL_LIST_ALIAS = "全局列表"
SUB_TEST_ALIAS = "订阅测试"


class _NotifyContext:
    def __init__(self, plugin: "BilibiliPlugin") -> None:
        self._plugin = plugin

    async def send_notification(self, notification: SubscriptionNotification) -> None:
        mode, payload = SubscriptionNotificationDispatcher.build_send_payload(notification)
        stream_id = notification.sub_user
        if mode == "forward":
            ok = await self._plugin.ctx.send.forward(payload, stream_id)
        else:
            ok = await self._plugin.ctx.send.hybrid(payload, stream_id)
        if not ok:
            raise RuntimeError(f"send failed: stream_id={stream_id}")

    async def generate_summary(self, prompt: str, sub_user: str) -> str:
        del sub_user
        result = await self._plugin.ctx.llm.generate(prompt)
        if isinstance(result, dict):
            return str(result.get("response") or result.get("content") or "").strip()
        return str(result or "").strip()


class BilibiliPlugin(MaiBotPlugin):
    config_model: ClassVar[type[BilibiliPluginConfig]] = BilibiliPluginConfig

    def __init__(self) -> None:
        super().__init__()
        self._started = False
        self._listener_task: asyncio.Task[Any] | None = None
        self._credential_task: asyncio.Task[Any] | None = None
        self.bili_client: BiliClient | None = None
        self.data_manager: DataManager | None = None
        self.logo_service: LogoService | None = None
        self.renderer: Renderer | None = None
        self.dynamic_listener: DynamicListener | None = None
        self.subscription_service: SubscriptionService | None = None
        self.subscription_page_service: SubscriptionPageService | None = None
        self.notification_dispatcher: SubscriptionNotificationDispatcher | None = None
        self.credential_lifecycle: CredentialLifecycle | None = None
        self._notify_context = _NotifyContext(self)
        self._hot_tool = BiliSearchHotVideosTool()
        self._dynamics_tool = BiliUserDynamicsTool()
        self._bgm_search_tool = BgmAdvancedSubjectSearchTool()
        self._bgm_hot_tool = BgmRecommendHotSubjectsTool()
        self._bgm_daily_tool = BgmDailyTool()

    async def on_load(self) -> None:
        await self._bootstrap()

    async def on_unload(self) -> None:
        await self._stop_tasks()

    async def on_config_update(self, scope: str, config_data: Dict[str, object], version: str) -> None:
        del scope, config_data, version
        await self._bootstrap(restart_tasks=True)

    def _cfg_dict(self) -> Dict[str, Any]:
        cfg = self.config
        return {
            "sessdata": cfg.credential.sessdata,
            "proxy": cfg.credential.proxy,
            "bangumi_token": cfg.credential.bangumi_token,
            "interval_secs": cfg.polling.interval_secs,
            "task_gap_secs": cfg.polling.task_gap_secs,
            "reconnect_silent": cfg.polling.reconnect_silent,
            "dynamic_limit": cfg.polling.dynamic_limit,
            "recent_dynamic_cache": cfg.polling.recent_dynamic_cache,
            "rai": cfg.render.rai,
            "renderer_template": cfg.render.renderer_template,
            "node": cfg.render.node,
            "plain_push_template": cfg.render.plain_push_template,
            "plain_push_forward_template": cfg.render.plain_push_forward_template,
            "enable_parse_miniapp": cfg.parse.enable_parse_miniapp,
            "enable_parse_BV": cfg.parse.enable_parse_BV,
            "enable_ai_summary": cfg.ai.enable_ai_summary,
            "ai_summary_prompt": cfg.ai.ai_summary_prompt,
        }

    async def _html_render(self, tmpl: str, data: Dict[str, Any], options: Dict[str, Any]) -> str | None:
        html = Template(tmpl).render(**data)
        viewport_height = int(options.get("viewport_height") or 1)
        result = await self.ctx.render.html2png(
            html,
            selector="body",
            viewport={"width": 900, "height": max(viewport_height, 1)},
            device_scale_factor=2.0,
            full_page=True,
            allow_network=True,
            wait_until="networkidle",
        )
        if not isinstance(result, dict):
            return None
        image_b64 = str(result.get("image_base64") or "").strip()
        if not image_b64:
            return None
        raw = base64.b64decode(image_b64)
        runtime = Path(self.ctx.paths.runtime_dir)
        runtime.mkdir(parents=True, exist_ok=True)
        fd, path = tempfile.mkstemp(prefix="bili-render-", suffix=".png", dir=str(runtime))
        try:
            with open(fd, "wb") as handle:
                handle.write(raw)
        except Exception:
            Path(path).unlink(missing_ok=True)
            raise
        return path

    async def _bootstrap(self, restart_tasks: bool = False) -> None:
        if not self.config.plugin.enabled:
            await self._stop_tasks()
            return

        cfg = self._cfg_dict()
        data_dir = self.ctx.paths.data_dir
        self.data_manager = DataManager(
            data_dir=data_dir,
            recent_dynamic_cache=int(cfg["recent_dynamic_cache"]),
        )
        self.logo_service = LogoService(default_path=LOGO_PATH, data_dir=data_dir)

        saved = self.data_manager.get_credential()
        proxy = str(cfg["proxy"] or "").strip()
        if saved:
            self.bili_client = BiliClient(credential_dict=saved, proxy=proxy)
        else:
            self.bili_client = BiliClient(sessdata=str(cfg["sessdata"] or ""), proxy=proxy)

        self.renderer = Renderer(
            self._html_render,
            bool(cfg["rai"]),
            style=str(cfg["renderer_template"] or DEFAULT_TEMPLATE),
            runtime_dir=str(self.ctx.paths.runtime_dir),
        )
        self.notification_dispatcher = SubscriptionNotificationDispatcher(
            context=self._notify_context,
            on_sent=self._on_subscription_notification_sent,
        )
        self.dynamic_listener = DynamicListener(
            context=self._notify_context,
            data_manager=self.data_manager,
            bili_client=self.bili_client,
            renderer=self.renderer,
            dispatcher=self.notification_dispatcher,
            cfg=cfg,
            logo_path_provider=self.logo_service.get_active_path,
        )
        self.subscription_service = SubscriptionService(
            data_manager=self.data_manager,
            bili_client=self.bili_client,
            parse_dynamics=self.dynamic_listener._parse_and_filter_dynamics,
        )
        self.subscription_page_service = SubscriptionPageService(
            data_manager=self.data_manager,
            subscription_service=self.subscription_service,
        )
        self.credential_lifecycle = CredentialLifecycle(
            client_supplier=lambda: self.bili_client,
            credential_saver=self.data_manager.set_credential,
            has_managed_credential=lambda: self.data_manager.get_credential() is not None,
        )

        token = str(cfg["bangumi_token"] or "").strip()
        self._hot_tool.bili_client = self.bili_client
        self._dynamics_tool.bili_client = self.bili_client
        self._dynamics_tool.parse_dynamics = self.dynamic_listener._parse_and_filter_dynamics
        self._bgm_search_tool.token = token
        self._bgm_hot_tool.token = token
        self._bgm_daily_tool.token = token

        self._configure_reconnect_silent()
        if restart_tasks or not self._started:
            await self._stop_tasks()
            self._start_tasks()
            self._started = True

    def _start_tasks(self) -> None:
        if self.dynamic_listener is None:
            return
        if self._listener_task is None or self._listener_task.done():
            self._listener_task = asyncio.create_task(
                self.dynamic_listener.start(), name="bilibili.dynamic_listener"
            )
        if self._credential_task is None or self._credential_task.done():
            self._credential_task = asyncio.create_task(
                self._credential_maintenance_loop(),
                name="bilibili.credential_maintenance",
            )

    async def _stop_tasks(self) -> None:
        for task in (self._listener_task, self._credential_task):
            if task and not task.done():
                task.cancel()
                try:
                    await task
                except asyncio.CancelledError:
                    pass
                except Exception as exc:
                    self.ctx.logger.error("停止后台任务失败: {err}".format(err=exc))
        self._listener_task = None
        self._credential_task = None
        self._started = False

    async def _credential_maintenance_loop(self) -> None:
        while True:
            try:
                if self.credential_lifecycle is not None:
                    result = await self.credential_lifecycle.refresh_if_required()
                    self._log_credential_refresh_result(result)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                self.ctx.logger.error("凭据维护失败: {err}".format(err=exc))
            await asyncio.sleep(CREDENTIAL_MAINTENANCE_INTERVAL_SECS)

    def _log_credential_refresh_result(self, result: Any) -> None:
        if result.status == CredentialRefreshStatus.REFRESHED:
            self.ctx.logger.info("Bilibili 凭据已刷新")
        elif result.status == CredentialRefreshStatus.FAILED:
            self.ctx.logger.warning("Bilibili 凭据刷新失败: {msg}".format(msg=result.message))

    def _configure_reconnect_silent(self) -> None:
        if self.notification_dispatcher is None or self.data_manager is None:
            return
        if not bool(self.config.polling.reconnect_silent):
            self.notification_dispatcher.set_silent_until_ts(0)
            return
        last_ts = self.data_manager.get_last_success_sub_notify_ts()
        now = int(time.time())
        if last_ts <= 0 or now - last_ts < RECONNECT_SILENT_THRESHOLD_SECS:
            self.notification_dispatcher.set_silent_until_ts(0)
            return
        silent_until = now + RECONNECT_SILENT_PADDING_SECS
        self.notification_dispatcher.set_silent_until_ts(silent_until)
        self.ctx.logger.info(
            "启用重连静默：距上次成功推送 {delta}s，静默至 {until}".format(delta=now - last_ts, until=silent_until)
        )

    async def _on_subscription_notification_sent(self, _notification: object) -> None:
        if self.data_manager is None:
            return
        await self.data_manager.set_last_success_sub_notify_ts(int(time.time()))

    @staticmethod
    def _parse_sub_args(input_text: str) -> tuple[List[str], List[str], bool, bool, bool, bool]:
        args = input_text.strip().split(" ") if input_text.strip() else []
        filter_types: List[str] = []
        filter_regex: List[str] = []
        live_atall = False
        at_all = False
        at_sub = False
        unat_sub = False
        for arg in args:
            if arg in VALID_SUB_OPTIONS:
                if arg == LIVE_ATALL_OPTION:
                    live_atall = True
                elif arg == AT_ALL_OPTION:
                    at_all = True
                elif arg == AT_SUB_OPTION:
                    at_sub = True
                elif arg == UNAT_SUB_OPTION:
                    unat_sub = True
                continue
            if arg in VALID_FILTER_TYPES:
                filter_types.append(arg)
            else:
                filter_regex.append(arg)
        return filter_types, filter_regex, live_atall, at_all, at_sub, unat_sub

    @staticmethod
    def _command_ok(message: str, intercept: bool = True):
        return True, message, intercept

    @staticmethod
    def _command_fail(message: str, intercept: bool = True):
        return False, message, intercept

    async def _reply_text(self, stream_id: str, text: str) -> None:
        if stream_id and text:
            await self.ctx.send.text(text, stream_id)

    async def _reply_image_and_text(self, stream_id: str, image_path: str, text: str = "") -> None:
        segments = [Image.fromFileSystem(image_path).to_send_segment()]
        if text:
            segments.append({"type": "text", "content": text})
        await self.ctx.send.hybrid(segments, stream_id)

    async def _apply_subscription(
        self,
        sub_user: str,
        uid_int: int,
        filter_types: List[str],
        filter_regex: List[str],
        live_atall: bool,
        at_all: bool | None = None,
        add_sub_users: List[str] | None = None,
        rm_sub_users: List[str] | None = None,
        inherit_filters: bool = False,
    ) -> tuple[bool, str]:
        assert self.subscription_service is not None
        result = await self.subscription_service.add_or_update(
            sub_user,
            uid_int,
            filter_types,
            filter_regex,
            live_atall,
            at_all=at_all,
            add_sub_users=add_sub_users,
            rm_sub_users=rm_sub_users,
            inherit_filters=inherit_filters,
        )
        if result.updated:
            option_desc = "开启" if live_atall else "关闭"
            return True, "该动态已订阅，已更新过滤条件。直播@全体: {opt}".format(opt=option_desc)
        return False, ""

    def _build_filter_desc(
        self,
        filter_types: List[str],
        filter_regex: List[str],
        live_atall: bool,
        at_all: bool = False,
        at_sub_users_len: int = 0,
    ) -> str:
        parts: List[str] = []
        if filter_types:
            parts.append('过滤类型' + ': ' + ', '.join(filter_types))
        if filter_regex:
            parts.append('过滤正则' + ': ' + str(filter_regex))
        parts.append('直播开播@全体' + ': ' + ('开启' if live_atall else '关闭'))
        parts.append('@全体成员' + ': ' + ('开启' if at_all else '关闭'))
        parts.append('@特定订阅者人数' + ': ' + str(at_sub_users_len))
        if not parts:
            return ""
        return "\n" + "\n".join(parts)

    @Command("bili_login", description="Scan login Bilibili", pattern=r"^/bili_login$")
    async def bili_login(self, stream_id: str = "", is_local_operator: bool = False, **kwargs: Any):
        if not is_local_operator:
            return self._command_fail('仅管理员可使用 /bili_login')
        message = kwargs.get("message") if isinstance(kwargs.get("message"), dict) else {}
        if bool(message.get("is_group_message")):
            return self._command_fail('仅支持管理员在私聊中使用 /bili_login')
        try:
            login_client = BiliQrLoginClient(proxy=self.config.credential.proxy)
            qrcode_info = await login_client.generate_qrcode()
            await self._reply_text(stream_id, '请使用 Bilibili App 扫描下方二维码登录：')
            await self.ctx.send.image(Image.fromFileSystem(qrcode_info.image_path).content, stream_id)
            while True:
                result = await login_client.poll()
                if result.is_done:
                    assert self.bili_client is not None and self.data_manager is not None
                    self.bili_client.set_credential(result.credential)
                    await self.data_manager.set_credential(result.credential)
                    self._start_tasks()
                    await self._reply_text(stream_id, '✅ 登录成功！')
                    break
                if result.is_timeout:
                    await self._reply_text(stream_id, '❌ 登录超时，请重新执行 /bili_login。')
                    break
                await asyncio.sleep(2)
            return self._command_ok("login finished")
        except BiliLoginError as exc:
            await self._reply_text(stream_id, '❌ 登录失败: ' + str(exc))
            return self._command_fail(str(exc))
        except Exception as exc:
            self.ctx.logger.error(f"login failed: {{exc}}")
            await self._reply_text(stream_id, '❌ 登录失败: ' + str(exc))
            return self._command_fail(str(exc))
    @Command("bili_logout", description="Logout Bilibili", pattern=r"^/bili_logout$")
    async def bili_logout(self, stream_id: str = "", is_local_operator: bool = False, **kwargs: Any):
        del kwargs
        if not is_local_operator:
            return self._command_fail('仅管理员可使用 /bili_logout')
        assert self.bili_client is not None and self.data_manager is not None and self.dynamic_listener is not None
        has_credential = self.bili_client.credential is not None
        server_logout = has_credential and await self.bili_client.logout()
        self.bili_client.credential = None
        await self.data_manager.clear_credential()
        self.bili_client = BiliClient(
            sessdata=self.config.credential.sessdata,
            proxy=self.config.credential.proxy,
        )
        self.dynamic_listener.bili_client = self.bili_client
        self._start_tasks()
        if not has_credential:
            msg = '当前没有已保存的登录凭据，本地配置已重置。'
        elif server_logout:
            msg = '✅ 已登出 Bilibili：服务端登录态已注销，本地凭据已清除。'
        else:
            msg = '⚠️ 服务端注销未成功（凭据可能已失效），已清除本地保存的凭据。'
        await self._reply_text(stream_id, msg)
        return self._command_ok(msg)
    @Command(
        "bili_card_style",
        description="Switch dynamic card style",
        pattern=rf"^/(?:bili_card_style|{CARD_STYLE_ALIAS})(?:\s+(?P<style>\S+))?$",
    )
    async def bili_card_style(self, stream_id: str = "", is_local_operator: bool = False, **kwargs: Any):
        if not is_local_operator:
            return self._command_fail('仅管理员可切换卡片样式')
        matched = kwargs.get("matched_groups") if isinstance(kwargs.get("matched_groups"), dict) else {}
        style = str(matched.get("style") or "").strip()
        available = get_template_names()
        if not style:
            lines = ['📋 可用的卡片样式：']
            for tid in available:
                info = CARD_TEMPLATES[tid]
                current = ' ← 当前' if tid == self.config.render.renderer_template else ""
                lines.append(f"  • {{tid}}: {{info['name']}}{{current}}")
                lines.append(f"    {{info['description']}}")
            lines.append("\n" + '使用 /卡片样式 <样式名> 切换')
            msg = "\n".join(lines)
            await self._reply_text(stream_id, msg)
            return self._command_ok(msg)
        if style not in available:
            return self._command_fail("样式 '{style}' 不存在。可用样式：" + ', '.join(available))
        self.config.render.renderer_template = style  # type: ignore[assignment]
        if self.renderer is not None:
            self.renderer.style = style
        info = CARD_TEMPLATES[style]
        msg = '✅ 已切换样式为：' + f"{info['name']} ({style})"
        await self._reply_text(stream_id, msg)
        return self._command_ok(msg)
    @Command(
        "bili_sub",
        description="Subscribe UP dynamics",
        pattern=rf"^/(?:bili_sub|{SUB_ALIAS})\s+(?P<uid>\d+)(?P<rest>.*)$",
    )
    async def bili_sub(self, stream_id: str = "", **kwargs: Any):
        matched = kwargs.get("matched_groups") if isinstance(kwargs.get("matched_groups"), dict) else {}
        uid = str(matched.get("uid") or "").strip()
        rest = str(matched.get("rest") or "").strip()
        filter_types, filter_regex, live_atall, at_all, at_sub, unat_sub = self._parse_sub_args(rest)
        if not uid.isdigit():
            return self._command_fail('UID 格式错误')
        uid_int = int(uid)
        message = kwargs.get("message") if isinstance(kwargs.get("message"), dict) else {}
        sender_id = str(((message.get("message_base_info") or {}).get("user_id")) or "").strip()
        inherit_filters = not filter_types and not filter_regex and (at_all or at_sub or unat_sub)
        add_sub_users = [sender_id] if at_sub and sender_id else None
        rm_sub_users = [sender_id] if unat_sub and sender_id else None
        updated, update_msg = await self._apply_subscription(
            stream_id,
            uid_int,
            filter_types,
            filter_regex,
            live_atall,
            at_all=at_all if at_all else None,
            add_sub_users=add_sub_users,
            rm_sub_users=rm_sub_users,
            inherit_filters=inherit_filters,
        )
        if updated:
            await self._reply_text(stream_id, update_msg)
            return self._command_ok(update_msg)
        assert self.bili_client is not None
        usr_info, msg = await self.bili_client.get_user_info(uid_int)
        if not usr_info:
            text = '订阅成功，但获取 UP 主信息失败: {msg}'.format(msg=msg)
            await self._reply_text(stream_id, text)
            return self._command_ok(text)
        filter_desc = self._build_filter_desc(
            filter_types,
            filter_regex,
            live_atall,
            at_all=at_all,
            at_sub_users_len=len(add_sub_users or []),
        )
        text = '📣 订阅成功！\nUP 主: {name} | 性别: {sex}{desc}\nhttps://space.bilibili.com/{mid}'.format(
            name=usr_info.get('name', 'Unknown'),
            sex=usr_info.get('sex', 'N/A'),
            desc=filter_desc,
            mid=usr_info.get('mid', uid_int),
        )
        await self._reply_text(stream_id, text)
        return self._command_ok(text)

    @Command("bili_sub_list", description="List subscriptions", pattern=rf"^/(?:bili_sub_list|{SUB_LIST_ALIAS})$")
    async def bili_sub_list(self, stream_id: str = "", **kwargs: Any):
        del kwargs
        assert self.data_manager is not None and self.bili_client is not None
        subs = self.data_manager.get_subscriptions_by_user(stream_id)
        if not subs:
            await self._reply_text(stream_id, '无订阅')
            return self._command_ok('无订阅')
        lines = ['订阅列表：']
        for idx, record in enumerate(subs):
            info, _ = await self.bili_client.get_user_info(int(record.uid))
            name = info["name"] if info else '无法获取 UP 主信息'
            lines.append(f"{{idx + 1}}. {{record.uid}} - {{name}}")
            filters = []
            if record.filter_types:
                filters.append('过滤类型' + ': ' + ', '.join(record.filter_types))
            if record.filter_regex:
                filters.append('过滤正则' + ': ' + str(record.filter_regex))
            filters.append('直播@全体' + ': ' + ('开启' if record.live_atall else '关闭'))
            filters.append('@全体成员' + ': ' + ('开启' if record.at_all else '关闭'))
            if record.at_sub_users:
                filters.append('@订阅者' + ': [' + ', '.join(record.at_sub_users) + ']')
            if filters:
                lines.append('   ' + '｜'.join(filters))
        msg = "\n".join(lines)
        await self._reply_text(stream_id, msg)
        return self._command_ok(msg)
    @Command(
        "bili_sub_del",
        description="Delete subscription",
        pattern=rf"^/(?:bili_sub_del|{SUB_DEL_ALIAS})\s+(?P<uid>\d+)$",
    )
    async def bili_sub_del(self, stream_id: str = "", **kwargs: Any):
        matched = kwargs.get("matched_groups") if isinstance(kwargs.get("matched_groups"), dict) else {}
        uid = str(matched.get("uid") or "").strip()
        if not uid.isdigit():
            return self._command_fail('参数错误，请提供正确的UID。')
        assert self.data_manager is not None
        ok = await self.data_manager.remove_subscription(stream_id, int(uid))
        msg = '删除成功' if ok else '未找到指定的订阅'
        await self._reply_text(stream_id, msg)
        return self._command_ok(msg) if ok else self._command_fail(msg)
    @Command("bili_global_list", description="List all subscriptions", pattern=rf"^/(?:bili_global_list|{GLOBAL_LIST_ALIAS})$")
    async def bili_global_list(self, stream_id: str = "", is_local_operator: bool = False, **kwargs: Any):
        del kwargs
        if not is_local_operator:
            return self._command_fail('仅管理员可查看全局订阅')
        assert self.data_manager is not None
        all_subs = self.data_manager.get_all_subscriptions()
        if not all_subs:
            await self._reply_text(stream_id, '当前没有任何订阅')
            return self._command_ok("empty")
        lines = ['全局订阅列表：']
        for umo, records in all_subs.items():
            lines.append(f"- {{umo}} ({{len(records)}})")
            for record in records:
                lines.append(f"  • {{record.uid}}")
        msg = "\n".join(lines)
        await self._reply_text(stream_id, msg)
        return self._command_ok(msg)
    @Command(
        "bili_global_del",
        description="Delete all subscriptions for a session",
        pattern=rf"^/(?:bili_global_del|全局删除)\s+(?P<raw>.+)$",
    )
    async def bili_global_del(self, stream_id: str = "", is_local_operator: bool = False, **kwargs: Any):
        if not is_local_operator:
            return self._command_fail("仅管理员可删除全局订阅")
        matched = kwargs.get("matched_groups") if isinstance(kwargs.get("matched_groups"), dict) else {}
        raw = str(matched.get("raw") or kwargs.get("text") or "").strip()
        if raw.startswith("/"):
            # strip command prefix if full text was passed
            parts = raw.split(maxsplit=1)
            raw = parts[1].strip() if len(parts) > 1 else ""
        umo = ""
        if raw.startswith("「"):
            end_idx = raw.find("」")
            if end_idx == -1:
                return self._command_fail("UMO/stream_id 格式错误")
            umo = raw[1:end_idx].strip()
        else:
            umo = raw.split()[0].strip() if raw else ""
        if not umo:
            return self._command_fail("请提供 stream_id/UMO")
        assert self.data_manager is not None
        msg = await self.data_manager.remove_all_for_user(umo)
        await self._reply_text(stream_id, str(msg))
        return self._command_ok(str(msg))

    @Command(
        "bili_global_sub",
        description="Add subscription for a session",
        pattern=rf"^/(?:bili_global_sub|全局订阅)\s+(?P<raw>.+)$",
    )
    async def bili_global_sub(self, stream_id: str = "", is_local_operator: bool = False, **kwargs: Any):
        if not is_local_operator:
            return self._command_fail("仅管理员可添加全局订阅")
        matched = kwargs.get("matched_groups") if isinstance(kwargs.get("matched_groups"), dict) else {}
        raw = str(matched.get("raw") or "").strip()
        umo = ""
        rest = ""
        if raw.startswith("「"):
            end_idx = raw.find("」")
            if end_idx == -1:
                return self._command_fail("UMO/stream_id 格式错误")
            umo = raw[1:end_idx].strip()
            rest = raw[end_idx + 1 :].strip()
        else:
            parts = raw.split(maxsplit=1)
            if not parts:
                return self._command_fail("用法: /bili_global_sub <stream_id> <uid> [filters]")
            umo = parts[0].strip()
            rest = parts[1].strip() if len(parts) > 1 else ""
        tokens = rest.split()
        if not tokens or not tokens[0].isdigit():
            return self._command_fail("请提供正确 UID")
        uid_int = int(tokens[0])
        filter_types, filter_regex, live_atall, at_all, at_sub, unat_sub = self._parse_sub_args(" ".join(tokens[1:]))
        updated, update_msg = await self._apply_subscription(
            umo,
            uid_int,
            filter_types,
            filter_regex,
            live_atall,
            at_all=at_all if at_all else None,
        )
        msg = update_msg if updated else f"已为 {umo} 订阅 {uid_int}"
        await self._reply_text(stream_id, msg)
        return self._command_ok(msg)

    @Command(
        "bili_sub_test",
        description="Test subscription push",
        pattern=rf"^/(?:bili_sub_test|{SUB_TEST_ALIAS})\s+(?P<uid>\d+)$",
    )
    async def bili_sub_test(self, stream_id: str = "", **kwargs: Any):
        matched = kwargs.get("matched_groups") if isinstance(kwargs.get("matched_groups"), dict) else {}
        uid = str(matched.get("uid") or "").strip()
        if not uid.isdigit():
            return self._command_fail('UID 必须是数字。')
        assert self.bili_client is not None and self.dynamic_listener is not None and self.data_manager is not None
        dyn = await self.bili_client.get_latest_dynamics(int(uid))
        if not dyn:
            return self._command_fail('未获取到动态数据，请稍后重试。')
        sub_data = self.data_manager.get_subscription(stream_id, int(uid)) or SubscriptionRecord(uid=int(uid))
        result_list = self.dynamic_listener._parse_and_filter_dynamics(dyn, sub_data)
        render_data = next((item.payload for item in result_list or [] if item.has_payload()), None)
        if not render_data:
            return self._command_fail('没有可用于测试推送的动态。')
        await self.dynamic_listener._handle_new_dynamic(stream_id, render_data, None, sub_data=sub_data)
        return self._command_ok('测试推送已触发', intercept=True)
    @EventHandler("bili_parse_bv", description="Parse BV / b23 links", event_type=EventType.ON_MESSAGE)
    async def parse_bv(self, message: Any = None, stream_id: str = "", **kwargs: Any):
        del kwargs
        if not self.config.parse.enable_parse_BV or not message:
            return True, True, None, None, None
        plain = message.get("plain_text", "") if isinstance(message, dict) else str(message)
        match_ = re.search(BV, plain or "", re.IGNORECASE)
        if not match_:
            return True, True, None, None, None
        assert self.bili_client is not None and self.renderer is not None and self.logo_service is not None
        if match_.group(2):
            converted = await self.bili_client.b23_to_bv(match_.group(0))
            if not converted:
                return True, True, None, None, None
            match_bv = re.search(r"(BV[a-zA-Z0-9]+)", converted, re.IGNORECASE)
            if not match_bv:
                return True, True, None, None, None
            bvid = match_bv.group(1)
        elif match_.group(1):
            bvid = match_.group(1)
        else:
            bvid = match_.group(0)
        video_data = await self.bili_client.get_video_info(bvid=bvid)
        if not video_data:
            await self._reply_text(stream_id, '获取视频信息失败了 (´;ω;`)')
            return True, True, None, None, None
        info = video_data["info"]
        online = video_data["online"]
        payload = RenderPayload(
            name="MaiBot",
            avatar=image_to_base64(self.logo_service.get_active_path()),
            title=info["title"],
            text=(
                f"UP 主: {info['owner']['name']}<br>"
                f"播放量: {info['stat']['view']}<br>"
                f"点赞: {info['stat']['like']}<br>"
                f"投币: {info['stat']['coin']}<br>"
                + '总共 {n} 人正在观看'.format(n=online['total'])
            ),
            image_urls=[info["pic"]],
        )
        img_path = await self.renderer.render_dynamic(payload)
        if img_path:
            await self._reply_image_and_text(stream_id, img_path)
        else:
            text = "\n".join(filter(None, payload.text.split("<br>")))
            await self.ctx.send.hybrid(
                [
                    {"type": "text", "content": '渲染图片失败了 (´;ω;`)' + "\n" + text},
                    {"type": "image", "content": info["pic"]},
                ],
                stream_id,
            )
        return True, True, None, None, None
    @EventHandler("bili_parse_miniapp", description="Parse Bilibili miniapp share", event_type=EventType.ON_MESSAGE)
    async def parse_miniapp(self, message: Any = None, stream_id: str = "", **kwargs: Any):
        del kwargs
        if not self.config.parse.enable_parse_miniapp or not isinstance(message, dict):
            return True, True, None, None, None
        assert self.bili_client is not None
        segments = message.get("message_segments") or []
        for segment in segments:
            data = segment.get("data") if isinstance(segment, dict) else None
            if not isinstance(data, dict):
                continue
            raw = data.get("data") or data.get("content") or data
            try:
                parsed = raw if isinstance(raw, dict) else json.loads(str(raw))
            except Exception:
                continue
            if not isinstance(parsed, dict):
                continue
            meta = parsed.get("meta", {})
            detail_1 = meta.get("detail_1", {}) if isinstance(meta, dict) else {}
            if detail_1.get("title") == "哔哩哔哩" and detail_1.get("qqdocurl"):
                url = detail_1.get("qqdocurl")
                if "https://b23.tv" in str(url):
                    url = await self.bili_client.b23_to_bv(str(url))
                await self._reply_text(stream_id, '标题' + ': ' + str(detail_1.get('desc')) + '\n' + '链接' + ': ' + str(url))
                return True, True, None, None, None
            news = meta.get("news", {}) if isinstance(meta, dict) else {}
            if news.get("tag") == "哔哩哔哩" and news.get("jumpUrl"):
                url = news.get("jumpUrl")
                if "https://b23.tv" in str(url):
                    url = await self.bili_client.b23_to_bv(str(url))
                await self._reply_text(stream_id, '标题' + ': ' + str(news.get('title')) + '\n' + '链接' + ': ' + str(url))
                return True, True, None, None, None
        return True, True, None, None, None
    @Tool(
        "bili_search_hot_videos",
        description="Search Bilibili hot videos or keyword videos",
        parameters=[
            ToolParameterInfo(name="keyword", param_type=ToolParamType.STRING, description="optional keyword", required=False),
            ToolParameterInfo(name="sort", param_type=ToolParamType.STRING, description="sort", required=False),
            ToolParameterInfo(name="limit", param_type=ToolParamType.INTEGER, description="limit", required=False),
            ToolParameterInfo(name="page", param_type=ToolParamType.INTEGER, description="page", required=False),
            ToolParameterInfo(name="tid", param_type=ToolParamType.INTEGER, description="zone tid", required=False),
        ],
    )
    async def tool_hot_videos(self, keyword: str = "", sort: str = "hot", limit: int = 8, page: int = 1, tid: Optional[int] = None, **kwargs: Any):
        del kwargs
        content = await self._hot_tool.call(keyword=keyword, sort=sort, limit=limit, page=page, tid=tid)
        return {"name": "bili_search_hot_videos", "content": content}

    @Tool(
        "bili_get_user_dynamics",
        description="Get recent dynamics of a Bilibili UP",
        parameters=[
            ToolParameterInfo(name="uid", param_type=ToolParamType.INTEGER, description="UP uid", required=True),
            ToolParameterInfo(name="limit", param_type=ToolParamType.INTEGER, description="limit", required=False),
        ],
    )
    async def tool_user_dynamics(self, uid: int = 0, limit: int = 3, **kwargs: Any):
        del kwargs
        content = await self._dynamics_tool.call(uid=int(uid), limit=int(limit))
        return {"name": "bili_get_user_dynamics", "content": content}

    @Tool(
        "bgm_advanced_subject_search",
        description="Search Bangumi subjects",
        parameters=[
            ToolParameterInfo(name="keyword", param_type=ToolParamType.STRING, description="keyword", required=True),
            ToolParameterInfo(name="filters", param_type=ToolParamType.OBJECT, description="filters", required=False),
            ToolParameterInfo(name="sort", param_type=ToolParamType.STRING, description="sort", required=False),
            ToolParameterInfo(name="limit", param_type=ToolParamType.INTEGER, description="limit", required=False),
            ToolParameterInfo(name="offset", param_type=ToolParamType.INTEGER, description="offset", required=False),
        ],
    )
    async def tool_bgm_search(
        self,
        keyword: str = "",
        filters: Optional[Dict[str, Any]] = None,
        sort: str = "match",
        limit: int = 8,
        offset: int = 0,
        **kwargs: Any,
    ):
        del kwargs
        content = await self._bgm_search_tool.call(
            keyword=keyword or "",
            filters=filters,
            sort=sort,
            limit=limit,
            offset=offset,
        )
        return {"name": "bgm_advanced_subject_search", "content": content}

    @Tool(
        "bgm_recommend_hot_subjects",
        description="Recommend Bangumi hot subjects",
        parameters=[
            ToolParameterInfo(name="months", param_type=ToolParamType.INTEGER, description="recent months", required=False),
            ToolParameterInfo(name="filters", param_type=ToolParamType.OBJECT, description="filters", required=False),
            ToolParameterInfo(name="limit", param_type=ToolParamType.INTEGER, description="limit", required=False),
            ToolParameterInfo(name="offset", param_type=ToolParamType.INTEGER, description="offset", required=False),
        ],
    )
    async def tool_bgm_hot(
        self,
        months: int = 3,
        filters: Optional[Dict[str, Any]] = None,
        limit: int = 8,
        offset: int = 0,
        **kwargs: Any,
    ):
        del kwargs
        content = await self._bgm_hot_tool.call(
            months=months,
            filters=filters,
            limit=limit,
            offset=offset,
        )
        return {"name": "bgm_recommend_hot_subjects", "content": content}

    @Tool(
        "bgm_daily_calendar",
        description="Bangumi daily calendar",
        parameters=[
            ToolParameterInfo(name="day", param_type=ToolParamType.STRING, description="today/monday..sunday", required=False),
            ToolParameterInfo(name="limit", param_type=ToolParamType.INTEGER, description="limit", required=False),
        ],
    )
    async def tool_bgm_daily(self, day: str = "today", limit: int = 8, **kwargs: Any):
        del kwargs
        content = await self._bgm_daily_tool.call(day=day, limit=limit)
        return {"name": "bgm_daily_calendar", "content": content}

    @API("page_list_subscriptions", description="List subscriptions", version="1")
    async def page_list_subscriptions(self, keyword: str = "") -> Dict[str, Any]:
        assert self.subscription_page_service is not None
        return self.subscription_page_service.list_subscriptions(keyword)

    @API("page_subscription_status", description="Subscription summary", version="1")
    async def page_subscription_status(self) -> Dict[str, Any]:
        assert self.subscription_page_service is not None
        summary = self.subscription_page_service.list_subscriptions("").get("summary", {})
        return {"summary": summary, "style": self.config.render.renderer_template}

    @API("page_logo", description="Logo status", version="1")
    async def page_logo(self) -> Dict[str, Any]:
        assert self.logo_service is not None
        return self.logo_service.get_status()

    @API("page_logo_reset", description="Reset logo", version="1")
    async def page_logo_reset(self) -> Dict[str, Any]:
        assert self.logo_service is not None and self.dynamic_listener is not None
        result = await self.logo_service.reset()
        self.dynamic_listener.clear_render_cache()
        return result

    @API("page_up_info", description="UP info", version="1")
    async def page_up_info(self, uid: str) -> Dict[str, Any]:
        assert self.bili_client is not None
        uid_int = parse_bilibili_uid(uid)
        info, msg = await self.bili_client.get_user_info(uid_int)
        if not info:
            raise ValueError(msg or '获取 UP 主信息失败')
        return {"uid": str(uid_int), "name": info.get("name"), "face": info.get("face"), "sex": info.get("sex")}

    @API("page_save_subscription", description="Save subscription", version="1")
    async def page_save_subscription(self, **payload: Any) -> Dict[str, Any]:
        assert self.subscription_page_service is not None
        result, existed = await self.subscription_page_service.save(payload)
        return {
            "existed": existed,
            "record": self.subscription_page_service.serialize_record(result.record),
            "initialized": result.initialized,
            "updated": result.updated,
        }

    @API("page_delete_subscription", description="Delete one subscription", version="1")
    async def page_delete_subscription(self, umo: str = "", uid: str = "") -> Dict[str, Any]:
        assert self.subscription_page_service is not None
        deleted = await self.subscription_page_service.delete_subscription(umo, uid)
        if not deleted:
            raise ValueError('未找到订阅')
        return {"deleted": True}

    @API("page_delete_subscription_session", description="Delete session subscriptions", version="1")
    async def page_delete_subscription_session(self, umo: str = "") -> Dict[str, Any]:
        assert self.subscription_page_service is not None
        deleted = await self.subscription_page_service.delete_session(umo)
        if not deleted:
            raise ValueError('未找到会话订阅')
        return {"deleted": True}


def create_plugin() -> BilibiliPlugin:
    return BilibiliPlugin()
