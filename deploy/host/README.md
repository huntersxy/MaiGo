# MaiGo 宿主部署（systemd + 静态二进制）

不依赖 Docker 的生产部署：GitHub Actions 在打 tag 时构建静态二进制并发布 Release，
服务器上的 `maigo-update.timer` 周期拉取 `releases/latest`，由 `deploy.sh` 解包到
`releases/<ver>/`、切换 `current` 软链、重启 `maigo.service`，健康检查失败自动回滚。
GitHub 侧没有任何服务器地址或登录凭据。

## 目录布局

```
/opt/maigo/
├── app/         实例目录（config data host bots logs plugins media stickers depends-data）
├── releases/    每次发布的产物，保留最近 3 个
├── current -> releases/<ver>/
├── runtime/     宿主 venv：plugin-runtime、pycompat（不入发布包）
├── deploy/      install.sh / deploy.sh / update.sh / migrate-from-docker.sh
├── incoming/    下载或手工投放的发布包
└── maigo.env    环境变量（640 root:maibot，含密钥）
```

`current` 只放发布产物（`maigo`、`dashboard/dist`、`pycompat-src`、`pycompat_sidecar.py`、
`desktop_pet/presets`）；用户数据与 venv 独立于版本，切换版本不动它们。

## 首次安装

```bash
sudo bash deploy/host/install.sh /opt/maigo
sudo vi /opt/maigo/maigo.env          # 填 MAIBOT_SECRET_KEY
sudo systemctl enable --now maigo
```

已有 Docker 部署时用 `migrate-from-docker.sh` 把命名卷数据搬进 `app/`：

```bash
sudo bash deploy/host/migrate-from-docker.sh /opt/maigo
```

## 发布

打 tag 推送后 CI 自动构建并发布 Release（含 `.sha256`）。服务器不接收推送，
`maigo-update.timer` 每 15 分钟检查一次 `releases/latest`，有新版本就下载并部署。

手动触发一次检查：

```bash
sudo /opt/maigo/deploy/update.sh          # 有新版就部署
sudo /opt/maigo/deploy/update.sh --dry-run
```

也支持离线包：把 `maigo-<ver>-linux-amd64.tar.gz` 放进 `incoming/` 后

```bash
/opt/maigo/deploy/deploy.sh /opt/maigo/incoming/maigo-1.4.5-linux-amd64.tar.gz
```

回滚：`deploy.sh --rollback` 切回上一个 release。

## 回退到 Docker

命名卷在迁移时不删除：

```bash
cd /opt/maigo && docker compose up -d
```
