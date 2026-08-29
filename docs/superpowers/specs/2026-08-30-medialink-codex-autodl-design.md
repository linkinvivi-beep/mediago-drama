# MediaLink：Codex 生图与 AutoDL MiniMax H3 视频设计

日期：2026-08-30  
状态：设计已确认，等待规格审阅  
上游基线：`mediago-dev/mediago-drama@f06641a`

## 1. 目标

MediaLink 是 MediaGo Drama 的 macOS Apple Silicon 定向分支。它完整保留原项目的漫剧生产工作流与核心数据关系，只替换视觉生成引擎，并完成必要的外部品牌更名：

- 图片统一使用 Codex 内置的 `$imagegen` 能力，共享用户当前 Codex/ChatGPT 登录与 Codex 使用额度，不调用 OpenAI Image API，也不要求 OpenAI API Key。
- 视频统一使用用户手动启动的 AutoDL 云 GPU，通过 SSH 隧道连接 ComfyUI MiniMax H3 工作流。
- 保留原项目的原文、剧本、人物、场景、道具、分镜、素材库、任务历史、剧集预览、Skills 和提示词模板工作流。
- 保留现有“优化提示词”开关、交互和历史记录；图片与视频根据目标引擎采用不同优化策略。
- 只交付 macOS Apple Silicon（arm64）版本。
- 避免大面积重构；复用现有生成目录、任务、资产、文档关联、通知和恢复基础设施。

## 2. 非目标

首个版本明确不做：

- 不删除或重写人物、场景、道具、分镜等 MediaGo Drama 核心工作流。
- 不把产品缩减成“单张图片生成视频”的简化工具。
- 不调用 OpenAI Images API，不增加 OpenAI API Key 设置。
- 不接入 MiniMax 云端 API；MiniMax H3 只通过 AutoDL 上的 ComfyUI 执行。
- 不负责 AutoDL 实例的开机、关机、续费、余额或计费管理。
- 不公开暴露 ComfyUI 端口。
- 不静默回退到原有图片或视频供应商。
- 不支持 Windows、Intel Mac 或 Linux 发行包。
- 不为更名而批量修改 Go module、内部 import、数据库表名、内部 MCP wire name 或与用户无关的历史标识。
- 不改写上游 Apache-2.0 许可，不删除上游版权与归属说明。

## 3. 设计原则

1. **工作流不变，引擎可替换。** 所有原有资源卡片和生成入口继续调用统一生成服务，差异收敛到路由与执行器。
2. **一次提交只有一个身份。** Codex item ID 和 ComfyUI `prompt_id` 都必须持久化；断线恢复查询原任务，不重复提交。
3. **生成结果进入正式素材库。** Codex 临时文件和 AutoDL 远程文件都不是最终数据源，结果必须校验并导入 MediaLink 管理的资产目录。
4. **能力先检查，再开放按钮。** Codex 生图能力、SSH/ComfyUI 状态以及 H3 工作流节点都要通过预检。
5. **错误透明。** 登录、额度、策略、网络、节点、模型、显存和输出错误分别呈现，不用模糊的“生成失败”掩盖原因。
6. **秘密不落普通数据库和日志。** SSH 私钥、密码或私钥口令交给 macOS Keychain；任务日志只保留脱敏后的连接信息。
7. **旧实例信息不可信。** 每次连接按当前设置建立并验证 SSH/ComfyUI；历史 SSH 别名、端口和运行日志不能作为当前实例在线的证据。

## 4. 保持不变的核心流程

MediaLink 继续支持以下完整链路：

```text
原始内容
  -> 剧本
  -> 人物 / 场景 / 道具文档
  -> 人物图 / 场景图 / 道具图
  -> 分镜文档
  -> 分镜参考图
  -> 分镜视频
  -> 素材库 / 任务历史 / 剧集预览
```

以下入口全部保留单次与原有批量能力：

- 人物图片
- 场景图片
- 道具图片
- 分镜图片
- 分镜视频
- 生成工作台和 Agent 发起的同类生成

生成完成后的现有文档关联、首条结果选中、任务通知、素材缓存和剧集预览行为继续由当前服务负责。

## 5. 总体架构

```text
React 现有资源/分镜界面
          |
          v
现有 Generation HTTP / MCP / Task Service
          |
          +--------------------+
          |                    |
          v                    v
 CodexImageJobRunner     ComfyH3VideoRunner
          |                    |
          v                    v
 Codex app-server       AutoDL SSH Tunnel
 imageGeneration item          |
          |                    v
          |              ComfyUI REST + WS
          |                    |
          +----------+---------+
                     v
         现有素材导入 / 文档关联 / 任务历史
```

不增加第二套“MediaLink 专用项目模型”。新增执行器实现现有生成路由所需的提交、查询、取消和结果导入接口。

## 6. 生成目录与供应商可见性

前端生成目录只暴露两条产品路由：

| 类型 | 显示名称 | 执行器 | 用户凭据 |
| --- | --- | --- | --- |
| 图片 | `Codex 生图` | `CodexImageJobRunner` | 当前 Codex/ChatGPT 登录 |
| 视频 | `AutoDL · MiniMax H3` | `ComfyH3VideoRunner` | AutoDL SSH + macOS Keychain |

原有供应商实现先保留在源码中，避免无意义删除和回归风险，但从 MediaLink 的目录、模型选择器、设置页和默认路由中隐藏。任何故障都不能自动切换到隐藏供应商。

## 7. Codex 图片执行器

### 7.1 连接方式

`CodexImageJobRunner` 直接使用项目已经具备的 Codex app-server JSON-RPC 基础设施，不通过 ACP 文本投影抓取聊天内容，也不解析终端输出。

执行前预检：

1. 内置 Codex 可执行文件可启动并完成 `initialize`。
2. `account/read` 返回当前 ChatGPT 登录账户。
3. 当前可用模型声明 `imageGeneration` 能力。
4. 本次引用的本地图片存在、可读，且属于允许访问的项目/素材路径。

预检仅查询能力，不触发图片生成，不消耗一次正式图片任务。

### 7.2 单次任务流程

```text
用户点击原“生成图片”按钮
  -> 服务端创建现有 image generation task
  -> CodexImageJobRunner 启动/复用受控 app-server session
  -> 创建生成 turn，明确调用 $imagegen 并附带引用图
  -> 监听 imageGeneration item 状态
  -> 读取 savedPath / revisedPrompt / failure
  -> 校验图片格式、尺寸和文件存在性
  -> 复制到 MediaLink 正式素材目录
  -> 复用现有资产导入与文档关联
  -> task completed + 原有通知/预览更新
```

完成判据不是聊天中出现“已生成”，而是收到成功的结构化 `imageGeneration` item，且 `savedPath` 指向的文件通过本地校验。

### 7.3 提示词与结果记录

- `prompt` 保存用户实际提交的内容。
- 图片不套用 MiniMax H3 视频提示词规则。
- 图片“优化提示词”关闭时，用户 prompt 直接交给 `$imagegen`；结构化 item 中的 `revisedPrompt` 持久化到任务元数据，供历史查看和复现。
- 图片“优化提示词”开启时，复用现有优化任务与历史交互，由 Codex 文本执行器使用图片定向指令生成 `optimizedPrompt`，再交给 `$imagegen`；item 的 `revisedPrompt` 仍作为引擎最终修订记录单独保存。
- 不把 `revisedPrompt` 反向覆盖人物、场景、道具或分镜源文档。

### 7.4 批量与并发

- 所有现有批量入口继续可用。
- 首版全局只运行一个 Codex 图片生成 job；批次中的任务顺序排队。
- 队列中的每一项都有独立 task ID、Codex thread/turn/item ID 和结果。
- 单项失败不自动换供应商；批次继续或停止沿用现有批量任务策略，并展示失败项。

### 7.5 图片错误映射

至少区分：

- Codex 未登录
- 内置 Codex/app-server 不可用
- 当前模型无 `imageGeneration` 能力
- Codex 使用额度或速率受限
- 内容策略拒绝
- 用户取消
- Codex item 失败
- `savedPath` 缺失、越界、不可读或不是有效图片
- 图片导入正式素材库失败

任何一种错误都写回原任务失败状态，不创建伪成功资产。

## 8. AutoDL 与 ComfyUI 连接层

### 8.1 设置

用户可配置：

- SSH 主机、端口和用户名
- 认证类型和 macOS Keychain 中的秘密引用
- ComfyUI 远程端口，默认 `6006`
- REF2VA / FL2VA 工作流配置

普通设置数据库只保存非秘密字段和 Keychain opaque reference。私钥、密码或私钥口令不能出现在数据库、API 响应、前端状态、命令行参数和日志中。

首版使用 macOS 原生 Keychain adapter。需要将 PEM 私钥交给 OpenSSH 时，只在应用私有临时目录创建权限为 `0600` 的短生命周期文件，SSH 进程完成读取后立即删除；异常退出时启动清理也必须移除遗留文件。

### 8.2 SSH 隧道

MediaLink 通过系统 OpenSSH 建立仅本机可访问的转发：

```text
127.0.0.1:<动态本地端口> -> AutoDL 127.0.0.1:6006
```

- 不要求 AutoDL 对公网开放 ComfyUI。
- 首次连接记录 SSH host fingerprint；后续 fingerprint 变化必须硬失败并提示用户确认，不能自动忽略。
- 采用有限指数退避重连，状态显示为离线、连接中、可用、等待重连或配置错误。
- AutoDL 实例关机时只显示“云 GPU 离线”；MediaLink 不尝试开机。
- 本地端口动态选择，避免占用用户已有的 `6006`。

### 8.3 ComfyUI 预检

隧道建立后检查：

- `/system_stats`
- `/object_info`
- `/queue`
- 当前启用工作流所需节点和模型

只有 ComfyUI 在线且至少一个工作流 profile 验证通过，视频生成按钮才可提交。历史日志、旧 SSH alias 或“端口曾监听”都不能代替本次健康检查。

## 9. MiniMax H3 工作流配置

### 9.1 API Workflow + 语义映射

MediaLink 导入的是 ComfyUI API-format workflow，不接受只包含 UI `nodes` / `links` 的画布 JSON 直接提交。

每个 workflow profile 由两部分组成：

1. 原始 API Workflow JSON。
2. 语义 manifest，将业务参数映射到节点输入：

```text
prompt            -> 提示词节点
referenceImages   -> REF2VA 或 FL2VA 输入节点
duration/frames   -> 帧数或时长节点
ratio/resolution  -> 宽高节点
seed              -> sampler seed
outputPrefix      -> 保存节点
```

业务代码不硬编码用户当前工作流的节点 ID。工作流升级时重新导入 JSON 并更新映射，不修改生成核心代码。

首版提供两个 profile 类型：

- `REF2VA`
- `FL2VA`

界面只显示通过节点、字段、模型和输出节点验证的 profile。引用素材数量与类型由 profile capability 和 H3 约束共同校验，不能把 FL2VA 的首尾帧规则套到 REF2VA，反之亦然。

### 9.2 视频任务流程

```text
选择分镜和参考素材
  -> 可选：执行 H3 提示词优化
  -> 创建现有 video generation task
  -> 经 SSH 隧道上传引用素材
  -> 用 profile manifest 注入 workflow 参数
  -> POST /prompt
  -> 立即持久化 prompt_id 与 workflow snapshot
  -> WebSocket 监听进度，HTTP history/queue 兜底
  -> 断线后以同一 prompt_id 恢复查询
  -> 获取输出 MP4
  -> 下载、校验并导入正式素材库
  -> 更新任务、分镜资源和剧集预览
```

提交响应中没有合法 `prompt_id` 时任务不得进入“已提交”。一旦持久化 `prompt_id`，任何断线和应用重启都只能恢复该任务，不能自动再次 POST `/prompt`。

### 9.3 视频输出校验

导入前至少验证：

- 文件存在且非空
- 容器可解析
- 有视频流
- 时长在合理范围内
- 文件复制进入 MediaLink 正式素材目录成功

远程输出保留策略首版不自动删除。后续若增加清理功能，必须先验证本地文件，再只删除对应远程输出。

### 9.4 视频错误映射

至少区分：

- AutoDL 实例离线
- SSH 认证、host key 或隧道失败
- ComfyUI 健康检查失败
- workflow 不是 API 格式
- 节点或模型缺失
- profile 映射字段失效
- 参考素材上传失败
- ComfyUI 拒绝 `/prompt`
- 排队、执行中断或远程任务消失
- CUDA OOM / 显存不足
- 输出节点未产生文件
- MP4 损坏或导入失败

错误不能静默替换模型、节点或工作流。

## 10. MiniMax H3 提示词优化

“优化提示词”仍由 Codex 文本执行器完成，但系统指令和上下文改为 MiniMax H3 定向，不调用 MiniMax 文本 API。

### 10.1 输入上下文

优化器读取：

- 当前完整分镜内容
- 关联人物文档及身份、服装和外观约束
- 关联场景文档
- 关联道具文档
- 用户选择的参考图片及其顺序和角色
- 目标 workflow profile
- 时长、画幅和分辨率

多参考素材使用稳定编号，如 `参考图1`、`参考图2`，编号顺序同时传给优化器与 workflow manifest，避免提示词与输入槽位错位。

### 10.2 输出要求

H3 优化结果包含：

- 时长与画幅
- 人物身份、身体、服装锁定
- 场景和道具连续性
- 按时间顺序的动作
- 镜头运动、景别、构图、光线和视觉风格
- 参考素材映射
- 负面约束与结尾状态

多镜头内容使用“主时间线 + 每镜头微时间线”。总长度不超过 7000 字符，时长限制为 4–15 秒；引用总量遵守 H3 与当前 workflow profile 的共同上限。

若输入已经是一条完整、结构化且合法的 H3 提示词，只做约束校验和必要修正，不进行破坏性重写。

优化后的正文保存到现有 `optimizedPrompt`，继续出现在原提示词优化历史中。

## 11. 数据与恢复

优先复用当前 generation task、attempt、reference、asset 和 metadata 能力。仅当现有字段无法可靠表达时，添加向后兼容的增量字段或元数据键，不另建平行任务系统。

图片任务需持久化：

- Codex thread/turn/item ID
- 原始 prompt
- `revisedPrompt`
- Codex `savedPath`（诊断用途）
- MediaLink 正式资产路径
- 关联项目、文档、section 和资源类型

视频任务需持久化：

- ComfyUI `prompt_id`
- 连接配置 ID，不保存秘密
- workflow profile ID、版本和提交快照
- prompt / `optimizedPrompt`
- 有序引用素材及远程上传名
- 时长、画幅、分辨率、seed
- 远程输出描述与正式资产路径

恢复状态机至少覆盖：

```text
queued -> preparing -> submitted -> running -> importing -> completed
                         |             |           |
                         +-------> waiting_reconnect
                                       |
                                       +-------> failed / canceled
```

- `waiting_reconnect` 不是失败，也不能触发重新提交。
- 用户手动重试创建新 attempt，并明确关联旧 attempt。
- 应用启动后扫描未终结任务，Codex 图片 job 按结构化 item 身份恢复；ComfyUI job 按 `prompt_id` 恢复。
- 如果外部任务已无法证明存在，标记为需要用户处理，不能猜测成功或失败后自动重发。

## 12. 界面与设置

保留现有资源卡片、生成对话框、批量任务、历史和预览布局。设置页新增或收敛为：

### 12.1 Codex 生图

- 当前 Codex/ChatGPT 登录状态
- 登录、退出和刷新状态
- `imageGeneration` 能力状态
- 不生成图片的“测试连接”
- 无 API Key 输入框

### 12.2 AutoDL 云 GPU

- SSH 非秘密字段
- Keychain 凭据创建、替换和清除
- ComfyUI 远程端口
- 实时连接与健康状态
- 手动“重新连接”和“测试连接”
- 明确提示实例需要用户在 AutoDL 手动开机

### 12.3 MiniMax H3 工作流

- 导入 API Workflow
- 配置语义节点映射
- 保存 REF2VA / FL2VA profile
- 验证节点、模型和输出
- 展示 profile 版本、最近验证时间和失败原因

批量图片显示队列位置。视频断线显示“等待重连”，不把等待状态显示成新提交按钮。

## 13. 品牌、目录与发行

用户可见品牌统一为 `MediaLink`：

- 应用名、窗口标题和 About
- 图标和 DMG 视觉
- `.app`、DMG、ZIP 和 artifact 文件名
- Bundle ID：`app.medialink.desktop`
- 默认用户数据与 workspace 根目录：`~/Library/Application Support/MediaLink`

旧 MediaGo Drama 用户目录不自动移动、不删除、不就地改写。用户需要复用旧项目时，通过现有项目打开/注册能力显式选择，避免两个应用争用同一数据库。

为减少重构，以下内部名称首版允许保留：

- Go module/import 路径
- sidecar 二进制和部分内部命令名
- 数据库表名与 wire constants
- 与旧文档格式兼容有关的 `mgmd` 标识

自动更新不能继续指向上游 MediaGo Drama release。MediaLink 尚未配置自己的发行仓库前，禁用发布和自动更新入口。

仓库继续保留 Apache-2.0 LICENSE、上游版权和清晰的 fork attribution；README 明确说明 MediaLink 不是 MediaGo 官方发行版。

构建和 CI 只以 `darwin-arm64` 为发布目标。Windows 代码无需为本版本重写，但不再出现在 MediaLink 的构建、发布矩阵和用户文档中。

## 14. 测试与验收

### 14.1 自动化测试

- 使用 fake Codex app-server 测试初始化、账户、能力、imageGeneration item、取消、失败、`savedPath` 和 `revisedPrompt`。
- 测试 Codex 图片全局串行队列和批量单项失败。
- 使用 fake SSH process 和 fake Keychain adapter 测试秘密脱敏、临时密钥清理、host key 变化和重连退避。
- 使用 fake ComfyUI HTTP/WebSocket server 测试预检、工作流映射、上传、`/prompt`、进度、断线恢复、OOM 和输出下载。
- 对“已持久化 `prompt_id` 后断线”增加不可重复 POST 的回归测试。
- 覆盖人物、场景、道具、分镜单次和批量入口的现有回归。
- 覆盖 `optimizedPrompt`、引用顺序和 workflow snapshot 持久化。
- 不在自动化测试中触发真实 Codex 生图或真实 AutoDL GPU 任务。

### 14.2 工程质量门

- 相关 Go 单元测试与 race 测试通过。
- React/Vitest、lint、format 和 TypeScript build 通过。
- Electron main/preload IPC 测试通过。
- macOS arm64 `.app`、DMG 和 ZIP 构建成功。
- 安装后验证启动、登录状态、Keychain、workspace 路径和重启恢复。
- 检查最终 diff，确认没有大面积内部 rename 或无关格式化。

### 14.3 真实生成验收

真实验收至少覆盖：

1. 人物、场景、道具、分镜各一张 Codex 图片，自动落入正确素材与文档关系。
2. 一组 Codex 批量图片按顺序执行。
3. 一条 REF2VA 视频。
4. 一条 FL2VA 视频。
5. 一次视频生成中的 SSH 断线与同 `prompt_id` 恢复。
6. 一次应用重启后的未完成任务恢复。

真实生成会消耗 Codex 使用额度和 AutoDL GPU 时间。执行前必须再次向用户说明测试项并取得单独确认；没有确认时只完成无消耗的连接、模拟和构建测试。

## 15. 实施边界与顺序

后续实施计划应按以下边界拆分，避免交叉重构：

1. 品牌常量与 macOS arm64 发行边界。
2. MediaLink 可见路由目录和设置收敛。
3. Codex image-generation app-server client 与串行 runner。
4. 图片任务导入、恢复和 UI 状态。
5. macOS Keychain 与 SSH tunnel manager。
6. ComfyUI workflow profile、验证和视频 runner。
7. H3 定向提示词优化与引用上下文。
8. 端到端回归、arm64 打包和无消耗验收。
9. 经用户单独确认后的真实生成验收。

每一步优先在现有组件内增加窄接口和适配器；只有测试证明当前结构无法表达需求时，才引入增量 schema 或新服务。

## 16. 最终验收标准

MediaLink 可以在 macOS Apple Silicon 上安装启动；用户使用现有 Codex/ChatGPT 登录完成所有人物、场景、道具和分镜图片生成，使用手动启动的 AutoDL ComfyUI MiniMax H3 完成分镜视频生成。所有结果进入原有素材、任务和剧集工作流；提示词优化符合 H3 约束；断线或重启不产生重复 GPU 任务；界面不暴露其他生成供应商，也不要求 OpenAI Image API Key。
