# MaiGo 宿主部署（systemd + 静态二进制）

不依赖 Docker 的生产部署：push `main` 后 GitHub Actions 构建静态二进制并更新 `rolling`
预发布，随即用受限 SSH 密钥通知服务器；服务器自己走镜像加速下载、按 API digest 校验、
切换并重启，健康检查失败自动回滚。仓库内不含任何服务器地址（地址与密钥都在 Secrets），
该密钥被 `command=` 锁成只能触发部署，拿不到 shell。

## 目录布局

```
/opt/maigo/
├── app/         实例目录（config data host bots logs plugins media stickers depends-data）
├── releases/    每次发布的产物，保留最近 3 个；滚动构建记为 dev-<短提交>
├── current -> releases/<id>/
├── runtime/     宿主 venv：plugin-runtime（不入发布包）
├── deploy/      install.sh / deploy.sh / update.sh / ci-trigger.sh / migrate-from-docker.sh
├── incoming/    下载或手工投放的发布包
└── maigo.env    环境变量（640 root:maibot，含密钥）
```

`current` 放发布产物（`maigo`、`dashboard/dist`、`desktop_pet/presets`、可选 `bundled-plugins`）；`deploy.sh` 会把随包插件同步到实例 `app/plugins/`，并保留现网 `config.toml` 与 `data/`。用户数据与 venv 仍独立于版本。

表达向量聚类在 Go 进程内执行。从使用 Python 兼容侧车的版本升级时，可以从
`maigo.env` 移除旧的 `MAIBOT_COMPAT_*` 设置，以及指向 `current/pycompat-src` 的
`MAIBOT_SOURCE_ROOT`。已有表达向量索引可继续使用。

## 首次安装

```bash
sudo bash deploy/host/install.sh /opt/maigo
sudo vi /opt/maigo/maigo.env          # 填 MAIBOT_SECRET_KEY
sudo systemctl enable --now maigo
```

已有 Docker 部署时用 `migrate-from-docker.sh` 把命名卷数据搬进 `app/`（卷与镜像保留）：

```bash
sudo bash deploy/host/migrate-from-docker.sh /opt/maigo
```

## 发布

| 通道 | 触发 | Release tag | 用途 |
| --- | --- | --- | --- |
| 滚动 | push `main` | `rolling`（prerelease，每次整包替换） | 日常迭代，自动部署 |
| 版本 | push `v*` tag | `v1.4.5` | 归档、指定版本部署 |

push `main` 的流程：CI 构建 → 更新 `rolling` → SSH 调 `ci-trigger.sh <提交号>` →
服务器 `update.sh --expect-commit` 确认 Release 里的提交匹配后，从
`MAIGO_GH_MIRRORS`（默认 `gh-proxy.com`、`ghfast.top`）下载 44MB 包，
用 GitHub API 的 sha256 digest 校验，再交给 `deploy.sh`。

手动触发：

```bash
sudo /opt/maigo/deploy/update.sh --dry-run   # 只看是否可更新
sudo /opt/maigo/deploy/update.sh             # 立即拉取并部署
sudo MAIGO_RELEASE=latest /opt/maigo/deploy/update.sh   # 改跟正式版本 Release
```

也支持离线包：把 `maigo-<ver>-linux-amd64.tar.gz` 放进 `incoming/` 后

```bash
/opt/maigo/deploy/deploy.sh /opt/maigo/incoming/maigo-1.4.5-linux-amd64.tar.gz
```

回滚：`deploy.sh --rollback` 切回上一个 release。重复发布按包指纹
（`.package.sha256`）判定，内容变了才会重新部署。

## 回退到 Docker

命名卷在迁移时不删除：

```bash
cd /opt/maigo && docker compose up -d
```
