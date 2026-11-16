# Oh My Orchestrator

**面向私人部署、跨设备运行的编程 Agent 协调器。** 每个项目在服务机上保留一个长期运行的协调者，由它把较大的工作分派给你自己设备上的 Agent。

[English](../README.md) · [Fleet 配置](../docs/fleet.md) · [开发指南](../docs/development.md) · [反馈与问题](https://github.com/ThraceShah/oh-my-orchestrator/issues)

本仓库是 [Orchestrator.inc](https://github.com/OrchestratorInc/agent-orchestrator) 的独立社区分支，复用原版 React 界面，并加入私人网页协调者和远程 worker 流程。本项目与 OrchestratorInc 无隶属或背书关系。上游的桌面安装包不包含本分支的改动。

## 工作方式

```text
你 ── 对话 ──> 项目协调者（服务机）
                      │
                  选择 worker、
                  Agent 和模型
                      │
         ┌────────────┴────────────┐
         ▼                         ▼
    worker 设备                worker 设备
    独立 worktree               独立 worktree
         └────────────┬────────────┘
                      │ 持久化报告
                      └──────> 协调者
```

- **每个项目只有一个协调者。** 它使用服务机上已登记的项目目录。停止或替换协调者时，会保留源目录和其中未提交的文件。
- **通过对话提出工作。** 你在协调者的聊天界面描述需求，由它判断是继续现有工作还是创建子任务。网页不提供人工直接创建 worker 任务的入口。
- **你选择协调者配置。** 选择服务机上的 Agent、模型和思考等级。worker 机器、worker Agent、模型与 effort 则由协调者按能力和项目就绪情况选择。
- **worker 在本机运行。** 每个 worker 在自己的目录创建 worktree 并启动 Agent。完成报告会带着稳定会话身份去重后返回父协调者。
- **Agent 安装和登录由你控制。** 在需要运行它的设备上手动安装并授权 Provider Agent；系统不会自动登录，也不会在机器间复制凭据。

## 私人网页模式

网页使用原版 Orchestrator 的 renderer、看板、聊天和终端，并提供项目协调者、worker 能力发现和服务机目录浏览。它不要求 AO SaaS 账号，也隐藏账号、账单、团队和托管执行入口。部署到门户时，由门户现有的认证保护访问。

daemon 的主 API 仍只监听本机回环地址。worker 通过单独配置的 fleet 入口连接；请将该入口限制在可信的私有网络中。不要将 daemon API 或 portal operator 直接暴露到公网。详见 [Fleet 配置与安全说明](../docs/fleet.md) 和 [门户部署指南](../deploy/portal-operator/README.md)。

## 从源码构建

当前仓库发布的是源代码，尚无本分支专属的桌面或服务端安装包。上游的二进制发行版并非本分支构建。

### 环境要求

- Go 1.27.4 或更新版本
- Node.js 24 和 npm 10
- Git
- 在每台需要运行 Agent 的设备上，手动安装并授权对应 Provider Agent CLI

### 构建 daemon

```bash
git clone https://github.com/ThraceShah/oh-my-orchestrator.git
cd oh-my-orchestrator
cd backend
go build -o ../out/ao ./cmd/ao
```

### 构建并运行私人网页

网页构建会启用连接真实 daemon 的私人模式。`dev:web` 是 fixture 预览，不是生产服务。

```bash
cd frontend
npm ci --ignore-scripts
npm run build:web
cd ../deploy/portal-operator
HOST=127.0.0.1 PORT=3012 FLEET_API_PORT=3011 node server.mjs
```

daemon 应另行通过持久进程管理器运行。私人部署时设置 `AO_CLOUD_OFFERING=off`、control 角色和 fleet 监听地址，并把 portal operator 放在经过认证的反向代理后。daemon API 必须保持 loopback 监听，不能直接公开 operator 的 loopback 端口。完整配置见[门户部署指南](../deploy/portal-operator/README.md)。

## 添加 worker 设备

1. 在各 worker 上手动安装并授权所需的 Agent CLI。
2. 在服务机上运行 `ao fleet enroll` 注册 worker。Enrollment token 只显示一次，请写入权限受限的环境文件。
3. 启动 worker daemon，配置 `AO_FLEET_ROLE=worker`、节点 ID、服务机的私网 fleet URL 和 enrollment token。
4. 在每台 worker 上登记相同的逻辑项目 ID，并指向该机器本地的项目路径。ID 必须一致，路径留在各自机器上。
5. 分派任务前检查 Agent 授权和项目绑定状态是否新鲜且就绪。

命令示例和配置项见 [docs/fleet.md](../docs/fleet.md)。worker 离线、授权状态过期或项目未登记时，协调者会报告缺少的前置条件；它不会安装 Provider 或传递凭据。

## 开发检查

```bash
# Backend
(cd backend && go test ./...)

# Frontend
npm run frontend:typecheck
npm --prefix frontend test

# Portal 反向代理
node --test deploy/portal-operator/server.test.mjs
```

完整开发环境见[开发指南](../docs/development.md)。Fleet 和私人网页的专项验证记录见[验证说明](../docs/private-coordinator-validation.md)。

## 范围与状态

- 服务机协调者和 fleet 协议面向由项目所有者自行管理的设备。
- 私人网页目前只提供源码，尚无本分支的公开二进制发行版。
- 本分支尚未验证 Windows worker 部署；已登记但离线的节点不能作为可用 worker。
- 源码中仍保留上游桌面端和 Cloud 相关代码；这里介绍的私人网页模式不提供 AO SaaS 登录、账单或托管执行。

## 许可证

Apache-2.0。许可文本和沿用的版权声明见 [LICENSE](../LICENSE)。
