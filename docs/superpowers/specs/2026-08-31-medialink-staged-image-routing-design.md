# MediaLink 分阶段生图路线设计

日期：2026-08-31

状态：设计已口头通过，等待书面规格复核

## 1. 目标

在不等待 Qwen-Image-Edit-2511 安装、不继续依赖效果不理想的 FLUX 工作流的前提下，继续完成 MediaLink 的云端配置、Z-Image 生图、Codex 生图兜底和既有素材工作流集成。

本阶段必须保留 MediaGo Drama 的人物、场景、道具、分镜、素材、任务历史和文档关联流程，不做大面积重构。

## 2. 已确认的产品决定

- MediaLink 仍只面向 macOS Apple Silicon。
- 视觉生成入口保持三条公共路线：`Codex 生图`、`AutoDL · 云端生图`、`AutoDL · MiniMax H3`。
- `AutoDL · 云端生图` 本阶段只启用已经验证过的 `zimage-t2i` 与 `zimage-i2i`。
- Codex 使用内置 imagegen 能力，不接入 OpenAI Images API，也不增加 API Key 配置。
- FLUX 相关代码、历史配置和数据库记录不删除，但全部退出首版 readiness 与验收范围，默认停用。
- Qwen-Image-Edit-2511 与 Multiple Angles LoRA 暂不安装，不编造模型路径、ComfyUI 节点 ID、参数或 workflow。
- Qwen 在配置界面显示为“尚未安装”，不能进入实例调度、readiness 或任务提交。
- 当前 AutoDL 生图仍限制为零或一张参考图；只有 Qwen 实际安装并验证后，才调整为多图参考协议。
- “Codex 兜底”是用户可见的替代路线，不进行静默自动改派。AutoDL 等待或失败时，系统可以提示改用 Codex，但不得未经用户确认产生另一份生成任务。

## 3. 不变的现有基础

以下已经完成的能力继续作为唯一实现，不重写第二套：

- OpenSSH 登录命令解析与 macOS Keychain 密码存储。
- 多实例 SSH 隧道和动态本地端口。
- 仅允许 loopback origin 的 ComfyUI HTTP 客户端。
- 每实例一个图片/H3 共用执行槽的调度器。
- 自动分配、手动指定、重启 reservation 恢复和未知提交隔离。
- 现有 generation task、素材导入、人物/场景/道具/分镜关系和历史记录。

Qwen 后续接入时必须复用这些能力，不得新建 Qwen 专用 SSH、队列、下载或任务系统。

## 4. 分阶段能力模型

### 4.1 当前可用

| 引擎 | 能力 | 状态 |
| --- | --- | --- |
| Codex imagegen | 文生图、参考图驱动的内置生图 | 可配置和可预检 |
| Z-Image T2I | AutoDL 文生图 | 本阶段实现并启用 |
| Z-Image I2I | AutoDL 单参考图图生图 | 本阶段实现并启用 |
| MiniMax H3 | AutoDL 视频 | 继续复用共享实例池，按 H3 独立计划实现 |

### 4.2 保留但不可提交

| 引擎 | 状态 | 行为 |
| --- | --- | --- |
| FLUX / Lustly / Z→FLUX | 存储状态保持 `needs_revalidation`，界面标记“旧版/停用” | 不参与 readiness、自动选择或提交；保留已有记录 |
| Qwen-Image-Edit-2511 | 界面派生“尚未安装” | 仅展示计划能力，不创建伪 workflow profile |
| Multiple Angles LoRA | 界面派生“尚未安装” | 随 Qwen 一起后补，不单独进入调度 |

这些状态必须失败关闭。任何不可提交引擎都不能因为配置残留或旧 digest 被误判为 ready。

“旧版/停用”和“尚未安装”只是界面派生标签，不增加数据库状态枚举；Qwen 未安装时也不写入空 workflow 记录。

## 5. AutoDL 配置入口

设置页增加一个 MediaLink AutoDL 配置区域，沿用现有设置页结构，不建立独立管理应用。

### 5.1 实例配置

每个实例卡片显示：

- 实例名称与启用状态。
- 标准 SSH 登录命令。
- 远程 ComfyUI 端口，默认 6006，可手动修改。
- 密码“已保存/未保存”，密码字段只写不回显。
- 主机 fingerprint 的扫描、确认和变更状态。
- 当前连接状态、本地动态端口和执行槽占用摘要。
- 手动连接检查，但检查不得提交 `/prompt`。

实例地址和 SSH 端口允许变化；实例 profile ID 保持稳定。密码只进入 macOS Keychain。

### 5.2 生图引擎状态

配置区域显示：

- Z-Image T2I/I2I 的 workflow 导入、digest、实例验证结果和缺失节点/模型原因。
- Qwen-Image-Edit-2511：`尚未安装`，不显示虚构参数，只保留以后安装的入口位置。
- FLUX：收纳在“旧版/停用”区域，不默认展开，不允许误选为 ready。
- Codex imagegen 的现有预检状态。

设置响应永远不包含密码、远端原始响应、完整本地文件路径或任意公网 ComfyUI URL。

## 6. Z-Image workflow 设计

原 Task 6 缩小为一个通用 manifest 编译器和两个当前 profile：

- `zimage-t2i`：零参考图。
- `zimage-i2i`：恰好一张参考图。

编译器继续采用 manifest 驱动，而不是把节点 ID 写入 provider 业务代码。它负责：

- 校验 UI-format `nodes` 与 `links`。
- 编译为 ComfyUI API prompt map。
- 绑定提示词、seed、宽高、参考图、denoise 和输出前缀。
- 对原始 workflow 与 API template 分别计算稳定 digest。
- 每次实例化深拷贝 template，禁止修改存储快照。
- 用 `/object_info` 只读验证节点和模型可用性。
- 拒绝未声明输入、多余参考图、缺失输出和旧版错误 profile。

通用编译器不得包含 FLUX 或 Qwen 特有的硬编码。它以后通过新增 manifest 扩展，而不是修改提交、调度或下载流程。

## 7. 生成数据流

### 7.1 Z-Image

1. 用户在原有人物、场景、道具或分镜入口选择 `AutoDL · 云端生图`。
2. 零参考图选择 `zimage-t2i`，一张参考图选择 `zimage-i2i`，超过一张在 HTTP 提交前拒绝。
3. 可选提示词优化继续写入既有 `optimizedPrompt` 字段。
4. 调度器自动选择兼容实例，或只等待用户手动指定的实例。
5. 建立/复用隧道并实时验证 ComfyUI 与 profile。
6. I2I 上传唯一参考图；T2I 不上传图片。
7. 实例化 workflow，并且只提交一次 `/prompt`。
8. 取得 `prompt_id` 后立即绑定 reservation 并持久化实例、profile、digest 和提交时间。
9. 只在同一实例查询 history、下载和验证输出。
10. 通过现有素材导入事务写入素材库及人物/场景/道具/分镜关系。

未知提交结果必须隔离当前 lease，不能释放执行槽、改派实例或自动重提。

### 7.2 Codex 兜底

Codex 保持独立、用户明确选择的路线。AutoDL 不可用时，界面可以提供“改用 Codex 生图”的新尝试操作；新尝试保留对原任务的关联，但不能覆盖原任务或伪装成自动恢复。

## 8. Qwen 后续接入边界

Qwen 安装后再执行一个独立规格与计划，至少需要真实确认：

- Qwen-Image-Edit-2511 模型、文本编码器、VAE 和 LoRA 的准确文件名与目录。
- ComfyUI 实际节点类型、输入字段和多图连接方式。
- 当前 GPU 显存下可接受的最大参考图数量、尺寸和并发限制。
- Multiple Angles LoRA 的准确权重、触发词、强度和相机参数。
- 单图编辑、多图融合、人物一致性和角度控制的实际验收结果。

后续动态建图可以复用 `cloud_h3_batch_generate.mjs` 中“按参考素材增添节点、生成 API prompt、保存 prompt ID、查询 history、识别输出”的思路，但不能直接运行其 H3/PLOY 硬编码代码。

建议届时把 Qwen 建图提炼为一个纯 JSON 输入/输出的 builder：输入 prompt、参考图角色、相机参数和 workflow manifest，输出 API prompt 与模型需求。SSH、密码、提交、状态恢复、下载和素材导入仍由现有 Go 服务负责。

## 9. 错误与恢复

- 无兼容实例：保持 `waiting_for_instance`，不偷偷切换引擎。
- 手动实例不可用：保持 `waiting_for_selected_instance`，不 fallback。
- 缺失节点或模型：profile 标记不可用，不调用 `/prompt`。
- `/prompt` 结果不确定：隔离精确 lease/token，等待人工协调。
- 已有 `prompt_id`：只执行 Resume/Get，绝不再次 Generate。
- history 丢失：标记 `remote_task_lost`，不自动重生。
- 下载失败：保留远端身份和 reservation，根据可证明的远端状态恢复下载。
- 输出文件：验证类型、大小、尺寸、像素量和完整解码后才能进入素材库。

## 10. 测试与验收

本阶段默认只运行无消耗测试：

- Z-Image 两种 synthetic workflow 的 UI→API 编译和 digest 稳定性。
- 零/一/多参考图边界。
- 缺失节点、模型、输出和错误 binding 的失败关闭。
- 配置 API 的密码脱敏、动态端口、fingerprint revision 和 loopback URL 所有权。
- 两实例并行、单实例图片/H3 串行、手动实例不 fallback。
- submit-once、未知结果隔离、重启 Resume 和精确取消。
- Codex 与 AutoDL 路线互不覆盖，兜底必须创建用户确认的新尝试。
- 人物、场景、道具、分镜和素材关系的非回归测试。
- FLUX 和 Qwen 不得出现在 ready 候选中。

没有新的明确授权，不运行真实 `/prompt`。不得暂停或修改 `ComfyUIPhotoSync`。

## 11. 实施顺序

1. 修订 AutoDL 图像计划，使 FLUX 退出当前验收，Qwen 进入后续阶段。
2. 实现通用 workflow manifest 编译器及 Z-Image T2I/I2I。
3. 完成实例与引擎状态 API。
4. 完成 MediaLink AutoDL 配置界面。
5. 实现 Z-Image provider、恢复和素材导入。
6. 完成无消耗集成测试与 macOS arm64 验证。
7. Qwen 安装后另开独立规格，不回头重构前六步的通用基础。

## 12. 明确不做

- 当前不安装或下载 Qwen、Multiple Angles LoRA 或新的 FLUX 模型。
- 当前不生成 Qwen workflow，不猜测其节点和模型路径。
- 当前不修复、验证或启用七个 FLUX/混合 profile。
- 不删除旧 provider 源码或历史配置。
- 不增加 Windows、Intel Mac 或 Linux 发布目标。
- 不接入 OpenAI Images API。
- 不创建第二套 SSH、ComfyUI、调度、下载或素材导入系统。
