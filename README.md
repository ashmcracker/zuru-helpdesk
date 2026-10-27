# zuru-helpdesk — ZURU Helpdesk CLI (Go)

ZURU helpdesk 本地命令行工具（Go 单二进制）。让工程师 / AI 直接在终端查询 helpdesk 工单，无需打开网页。

认证走 ZURU helpdesk 授权服务器 + **Microsoft Entra**（ZURU 账号）登录，一次浏览器授权长期自动续期，无需复制 Cookie 或 API key。

---

## 特性

- **零三方依赖**：纯 Go 标准库，单二进制，开箱即用。
- **走 Entra**：`login` 跳转 Microsoft Entra 授权，token 自动刷新。
- **工单查询**：多条件过滤（状态 / 优先级 / 类型 / 服务 / 层级 / 标签 / 请求人 / 处理人 / SLA 超时 / 标题模糊）。

---

## 安装

从 GitHub Actions 下载编译产物（`zuru-helpdesk-windows-amd64.exe`），放入 PATH 目录即可。

---

## 用法

```bash
zuru-helpdesk login                  # 浏览器授权一次（走 ZURU Entra）
zuru-helpdesk whoami                 # 当前身份
zuru-helpdesk tickets --mine         # 我的工单
zuru-helpdesk tickets --status in_progress,blocked --priority p1,p2
zuru-helpdesk tickets --type CHANGE  # 按 ITIL 类型过滤
zuru-helpdesk ticket <id> --events   # 单详情 + 事件日志
zuru-helpdesk sla <id>               # SLA 状态
```

各子命令完整参数见 `<子命令> -h`。

---

## 本地构建

```bash
go build -trimpath -ldflags="-s -w" -o zuru-helpdesk.exe .
```

> 本机若被 EDR（Sophos）拦截 Go 工具链子进程，直接用 CI 产物即可。
