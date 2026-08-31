# MediaLink 可配置工作流注册设计

日期：2026-08-31

状态：等待书面规格复核

## 1. 目标与核心原则

MediaLink 保留 MediaGo Drama 已有的人物、场景、道具、分镜、素材、任务历史和文档关联流程，只替换生图、视频执行能力，不做大面积无意义重构。

云端模型与 ComfyUI 工作流会频繁变化，因此工作流必须是可导入、可验证、可版本化的配置数据，而不是写死在 provider、调度器或页面中的模型专用功能。产品提供统一的“添加工作流”入口；Z-Image、Qwen、FLUX 等名称只属于某个工作流配置的显示信息，不进入核心分支逻辑。

本阶段只面向 macOS Apple Silicon。

## 2. 公共生成路线

用户可见的三条公共路线保持不变：

- `Codex 生图`：使用 Codex 内置 imagegen，不接入 OpenAI Images API，也不增加 API Key 配置。
- `AutoDL · 云端生图`：使用本设计的通用工作流注册表。
- `AutoDL · MiniMax H3`：继续复用现有 AutoDL 实例池和每实例执行槽；本规格不扩展 H3 workflow 管理。

Codex 是用户明确选择的兜底路线。AutoDL 等待或失败时可以提示用户创建一次 Codex 新尝试，但不得静默改派、覆盖原任务或伪装成自动恢复。

## 3. 复用的现有基础

以下已经完成的能力继续作为唯一实现：

- OpenSSH 登录命令解析和 macOS Keychain 密码存储。
- 多实例 SSH 隧道及自动分配的本地 loopback 端口。
- 只接受 loopback origin 的 ComfyUI HTTP 客户端。
- 每实例一个由图片与 H3 共用的执行槽。
- 自动分配、手动指定、reservation 恢复和未知提交隔离。
- 现有 generation task、素材导入及人物、场景、道具、分镜关系。

新增工作流不得建立第二套 SSH、密码、HTTP、调度、下载、恢复或素材导入系统。

## 4. 工作流注册表

### 4.1 工作流配置

每个工作流配置至少保存：

- 稳定的 profile ID、显示名称、说明、媒体类型和所属公共路线。
- `enabled` 和 `autoSelectable`，分别控制能否被显式使用、能否参与自动选择。
- 参考图契约：最少与最多数量，或者固定且有顺序的参考图槽。
- 用户导入的 ComfyUI UI-format JSON 快照及其稳定 digest。
- 编译后的 ComfyUI API prompt template 快照及其稳定 digest。
- 用户确认过的语义绑定。
- 不可变版本号、创建时间、来源版本和归档状态。

语义绑定支持：

- 一个或多个提示词输入。
- 一个或多个 seed 输入。
- 宽、高。
- 有序参考图槽及其目标节点输入。
- denoise。
- 输出文件前缀。
- 一个或多个输出节点及输出角色。
- 工作流声明的少量标量参数，例如步数、CFG 或 LoRA 强度。

首版标量参数只允许有明确类型、范围、默认值和目标节点输入的 number、integer、boolean 或短字符串。它不是任意 JSON 修改器，不能改写 `class_type`、节点连线、模型路径、未声明字段或执行代码。

### 4.2 添加工作流

“添加工作流”采用以下流程：

1. 用户导入 ComfyUI UI-format JSON。
2. 有边界的解析器检查格式、大小、深度、节点和连线数量。
3. 编译器生成 API prompt template。
4. 系统根据节点类型、输入名称和图连接关系建议语义绑定。
5. 用户逐项确认或修正提示词、尺寸、seed、参考图、denoise 和输出映射。
6. 系统保存不可变 workflow 版本和两个 digest。
7. 系统对用户选定的实例执行只读验证。
8. 只有已启用且在目标实例验证通过的版本，才能进入 readiness 和调度候选。

自动建议不得被当作已确认映射。无法唯一确定的字段必须显示配置错误，不能静默猜测节点或输入。

### 4.3 替换、复制、停用与归档

- “替换工作流”创建新的不可变版本，不原地覆盖旧快照。
- 已创建的任务继续引用创建时的 profile ID、版本和 digest。
- 新版本不会继承旧版本的实例验证结果，必须重新验证。
- “复制”创建新的 profile，便于从现有配置调整绑定或参数。
- “停用”阻止新任务使用，但保留配置和历史任务。
- “归档”从日常选择列表隐藏；被任务引用的版本不得物理删除。
- 首版不提供破坏性删除入口。

## 5. 默认选择与兼容性

默认工作流由用户按参考图模式配置，不在核心代码中写死 Z-Image、Qwen 或 FLUX：

- 无参考图可设置一个默认 profile。
- 一张或多张参考图可分别设置兼容默认 profile。
- 用户也可以在生成时显式选择 profile。
- profile 的参考图契约必须与本次输入数量匹配。
- 自动选择只考虑 `enabled`、`autoSelectable` 且在实例验证通过的兼容版本。
- 没有兼容默认项，或同时出现多个无法判定优先级的默认项时，失败关闭并提示配置，不静默选择。

提示词优化继续使用 MediaGo Drama 原有入口和 `optimizedPrompt` 数据流。优化器应面向所选 profile 的提示词说明生成内容，不在 provider 中硬编码模型品牌规则。

## 6. 编译与运行边界

### 6.1 有边界的 JSON 编译器

编译器负责：

- 校验 UI-format `nodes`、`links` 和节点引用完整性。
- 确定性地编译为 ComfyUI API prompt map。
- 根据已确认绑定写入本次 prompt、seed、尺寸、参考图、denoise、输出前缀和公开标量参数。
- 每次实例化深拷贝 template，禁止修改持久化快照。
- 生成可重复计算的 workflow digest 与 template digest。

MediaLink 不执行用户上传的 JavaScript、`.mjs`、shell 或 Python。已有 `.mjs` 动态建图脚本只作为设计参考；可复用的是“动态构造 JSON、记录 prompt ID、查询 history、定位输出”的思路，而不是直接引入其 PLOY/H3 硬编码代码。

### 6.2 生成数据流

1. 用户从现有人物、场景、道具或分镜入口选择 `AutoDL · 云端生图`。
2. 系统根据参考图数量和显式选择解析唯一的 workflow profile 版本。
3. 调度器自动选择兼容实例，或只等待用户手动指定的实例。
4. 隧道和 ComfyUI 客户端连接目标实例，并检查该实例对当前版本的验证状态。
5. 需要时上传已绑定的参考图。
6. 编译器从不可变 template 实例化本次 API prompt。
7. 客户端只提交一次 `/prompt`。
8. 获得 `prompt_id` 后立即持久化 reservation、实例、profile、版本、digest 和提交时间。
9. 只在同一实例查询 history、下载并验证绑定的输出。
10. 通过现有素材事务写入素材库及人物、场景、道具或分镜关系。

已有 `prompt_id` 的任务只能 Resume/Get，不能再次 Generate。提交结果不确定时隔离精确 lease 和 token，不释放执行槽、不换实例、不自动重提。取消只能针对已知 prompt 身份，不调用会影响其他任务的全局 `/interrupt`。

## 7. 实例验证与 readiness

工作流验证是“profile 版本 × 实例”的只读结果，至少检查：

- ComfyUI `/object_info` 可访问。
- 每个 `class_type` 存在，已绑定输入属于对应节点。
- 必需模型和枚举选项在实例报告的允许值内。
- 参考图槽、提示词、尺寸、seed 和 denoise 映射有效。
- 至少一个已声明输出节点存在，并且输出角色可识别。
- 当前 workflow 与 template digest 与验证记录完全一致。

验证状态包括通过、失败、未知和过期。只有“通过”可进入 ready；导入新版本、修改绑定、实例 fingerprint 变化或 `/object_info` 能力摘要变化都会使旧验证过期。

验证不得提交 `/prompt`。失败、未知、过期或已停用的 profile 都不能参与调度。

## 8. 配置界面

设置页增加 `MediaLink 配置` 区域，沿用现有页面结构。

### 8.1 AutoDL 实例

每个实例卡片显示：

- 名称、启用状态和标准 SSH 登录命令。
- 远程 ComfyUI 端口；默认可填 6006，但允许每实例修改。
- 密码“已保存/未保存”；输入只写入 macOS Keychain，永不回显。
- 主机 fingerprint 的扫描、确认和变更状态。
- 当前连接、本地动态端口、执行槽和最近错误摘要。
- 自动分配开关，以及高级设置中的手动实例指定。
- 只读连接检查。

实例地址和 SSH 端口可以变化，稳定实例 profile ID 不随登录命令变化。

### 8.2 工作流

工作流区域提供：

- `添加工作流`、`替换版本`、`复制`、`启用/停用`、`归档`。
- profile 名称、参考图契约、当前版本和两个 digest 摘要。
- 语义映射审查页，包括系统建议、用户确认状态和参数范围。
- 每个实例的验证状态、时间以及缺失节点、模型、输入或输出原因。
- 无参考图、单参考图、多参考图的默认 profile 设置。

设置 API 永远不返回密码、远端原始响应、完整本地敏感路径或由 workflow 提供的公网 ComfyUI URL。工作流不得覆盖实例拥有的 loopback base URL。

## 9. 安全与资源限制

- 导入仅接受 JSON，并限制字节数、嵌套深度、节点数、连线数和单字段长度。
- 拒绝可执行脚本、任意 URL 拉取、shell 字段、插件安装指令和路径穿越。
- workflow 不能提供或修改 ComfyUI base URL；网络只能经过现有 loopback 客户端和受管隧道。
- 密码只存 Keychain；日志、配置响应和错误消息必须脱敏。
- 模型文件名只能来自导入快照与 `/object_info` 的精确匹配，MediaLink 不替用户下载或猜测模型。
- 输出在导入素材库前必须验证类型、大小、尺寸、像素量和完整解码。

## 10. 当前工作流的处理

- 现有 Z-Image 文生图和图生图 JSON 可以作为首批导入 profile，但它们不获得核心代码特权。
- FLUX 旧记录保留，可停用或归档；不修复、不默认启用，也不进入当前 readiness 验收。
- Qwen-Image-Edit-2511 与 Multiple Angles LoRA 安装后，使用相同“添加工作流”入口导入并映射；当前不创建虚假 profile，不猜节点、模型路径或多图协议。
- 以后更换任何 ComfyUI workflow，只新增或替换 profile 版本，不重构 provider、调度器或素材流程。

## 11. 错误与恢复

- 无兼容实例：保持 `waiting_for_instance`，不偷偷切换公共路线。
- 手动实例不可用：保持 `waiting_for_selected_instance`，不 fallback。
- 映射、节点、模型、输出或 digest 不一致：提交前失败关闭。
- `/prompt` 结果不确定：隔离当前 lease，等待人工协调。
- history 丢失：标记 `remote_task_lost`，不自动重生。
- 下载失败：保留远端身份和 reservation，只恢复可证明的下载步骤。
- workflow 被停用、归档或升级：不改变已提交任务引用的历史快照。

## 12. 测试与验收

默认只执行无消耗测试：

- UI JSON 导入、边界拒绝、确定性编译和 digest 稳定性。
- 自动建议与用户确认分离；含歧义的映射失败关闭。
- 提示词、seed、尺寸、参考图、denoise、输出及公开标量参数绑定。
- 参考图最少/最多/固定槽契约及错误数量拒绝。
- 替换产生新版本、旧任务快照不变、旧实例验证失效。
- 错误节点、模型、输入、输出、workflow digest 和 template digest。
- 默认项缺失或歧义时不静默选择。
- 导入内容不能执行 JS、`.mjs`、shell、任意 URL 或覆盖 base URL。
- 配置 API 密码脱敏、动态端口、fingerprint revision 和 loopback URL 所有权。
- 两实例并行、单实例图片/H3 串行、手动实例不 fallback。
- submit-once、未知结果隔离、重启 Resume 和精确取消。
- Codex 与 AutoDL 路线互不覆盖，兜底必须由用户确认创建新尝试。
- 人物、场景、道具、分镜、素材和历史关系非回归。

没有新的明确授权，不运行真实 `/prompt`，不暂停或修改 `ComfyUIPhotoSync`。

## 13. 实施顺序

1. 实现通用 workflow profile、不可变版本、digest 和实例验证数据结构。
2. 实现有边界的 UI JSON 导入、编译和语义绑定确认。
3. 实现实例与 workflow 配置 API。
4. 实现 `MediaLink 配置` 中的实例与工作流管理界面。
5. 将 `AutoDL · 云端生图` provider 接到 profile 解析、现有调度器、恢复和素材导入流程。
6. 把已有 Z-Image JSON 作为普通首批 profile 导入并完成无消耗验收。
7. 完成 macOS Apple Silicon 打包与核心 MediaGo Drama 流程回归。

Qwen 安装和具体 workflow 到位后只增加 profile 与实例验证，不改变上述核心结构。

## 14. 明确不做

- 当前不安装或下载 Qwen、Multiple Angles LoRA 或新 FLUX 模型。
- 当前不修复、验证或启用 FLUX 工作流。
- 当前不猜测任何尚未取得的节点、模型路径、参数或 workflow。
- 当前不运行真实云端生成或 `/prompt`。
- 不执行用户导入的 `.mjs` 或其他代码。
- 不删除旧 provider 源码、历史配置或已有任务引用。
- 不增加 Windows、Intel Mac 或 Linux 发布目标。
- 不接入 OpenAI Images API。
- 不创建第二套 SSH、ComfyUI、调度、下载或素材导入系统。
