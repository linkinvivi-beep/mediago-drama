# MediaLink macOS arm64 发布检查清单

日期：2026-09-01

本清单区分静态代码检查、离线构建验证和需要用户单独授权的真实生成验收。不得把“代码已实现”写成“应用已构建或工作流已跑通”。

## 1. 产品边界

- [x] 产品名为 `MediaLink`。
- [x] Electron bundle ID 配置为 `app.medialink.desktop`。
- [x] 构建目标配置仅接受 `darwin-arm64`。
- [x] 预期产物名为 `MediaLink-<version>-macos-arm64.dmg` 和 `MediaLink-<version>-macos-arm64.zip`。
- [x] 可见视觉路线为 `Codex 生图`、`AutoDL · 云端生图`、`AutoDL · MiniMax H3`。
- [x] Codex 图片路线使用当前 Codex 登录与内置生图能力，不调用 OpenAI Images API。
- [x] MediaLink 全局数据目录为 `~/Library/Application Support/MediaLink`；项目内部 `.mediago-drama` 标记保持兼容，不执行旧数据迁移。

以上项目仅依据当前源码和构建配置静态核对，未证明安装包能够启动。

## 2. AutoDL 与工作流前提

- [ ] 至少配置一个已启动的 AutoDL 实例。
- [ ] 填写该实例当前有效的完整 SSH 登录指令。
- [ ] 确认 SSH 主机指纹；密码仅保存到 macOS Keychain。
- [ ] 填写该实例实际的 ComfyUI 远端 loopback 端口。端口不固定为 `6006`。
- [ ] 导入目标 ComfyUI 工作流，并确认提示词、参考图、尺寸、时长、种子与输出等语义映射。
- [ ] 在准备使用的每个实例上验证对应工作流版本后再启用。
- [ ] 多实例自动调度与高级手动指定都只使用已验证的“实例 + 工作流版本”组合。

工作流可以是当前或后续安装的 Z-Image、Qwen-Image-Edit、FLUX、MiniMax H3 或其他兼容图；MediaLink 不根据文件名或模型名硬编码节点。

## 3. 离线质量门

以下命令在本轮未执行，因为用户已要求停止重复测试：

- [ ] `task -d packages/core check`
- [ ] `task -d packages/core test`
- [ ] `task -d services/server check`
- [ ] `task -d services/server test`
- [ ] `go build ./...`（目录：`services/server`）
- [ ] `pnpm check`（目录：`apps/workspace`）
- [ ] `pnpm test`（目录：`apps/workspace`）
- [ ] `pnpm build`（目录：`apps/workspace`）

已执行的有限检查：

- [x] 当前提交的 staged TypeScript 文件通过提交钩子的 `oxlint --fix` 与 `oxfmt`。
- [x] 本轮提交前执行 `git diff --check`，没有空白错误。
- [x] 未跟踪的 `work/` 目录保持未修改、未暂存。

## 4. Apple Silicon 构建与安装检查

本轮未构建安装包，以下项目仍待执行：

- [ ] 从仓库根目录准备 arm64 sidecar 与资源。
- [ ] 在 `apps/workspace` 执行 `pnpm electron:build:darwin-arm64`。
- [ ] 确认输出目录只出现 MediaLink arm64 DMG/ZIP，没有 Windows、Linux 或 Intel Mac 产物。
- [ ] 将 DMG 挂载到临时位置，不覆盖现有 MediaGo Drama 或 MediaLink 应用。
- [ ] 启动临时应用，检查窗口标题、图标、MediaLink 配置页和三条可见路线，再正常退出。
- [ ] 对应用主可执行文件运行 `file`，确认只包含 `arm64`。
- [ ] 运行 `codesign -dv --verbose=4`，确认 identifier 为 `app.medialink.desktop`。
- [ ] 运行 `spctl --assess --type execute`；若本地包未签名或未公证，记录真实失败，不宣称已公证。
- [ ] 确认只创建 `~/Library/Application Support/MediaLink`，旧 MediaGo Drama 数据目录时间戳和内容未变化。

## 5. 真实生成验收

状态：**未授权、未执行。**

- [ ] 获得用户对 Codex 图片额度和 AutoDL GPU 消耗的单独明确授权。
- [ ] 运行一个低成本测试图片任务，确认 Codex item、修订提示词、素材导入和人物/场景/道具关联。
- [ ] 运行一个用户批准工作流的 4 秒 H3 测试任务，记录唯一 ComfyUI `prompt_id`，验证断线后按原 ID 恢复，不重复提交。
- [ ] 验证输出进入素材库、分镜选择和分集预览。
- [ ] 测试结果使用独立测试资产，不替换已经确认的正式素材。

真实验收记录不得包含密码、SSH 私钥、完整带签名 URL、账户余额或其他凭据。

## 6. 当前结论

当前代码具备进入离线质量门和 arm64 构建检查的条件，但这些检查尚未执行，因此本清单不将 MediaLink 标记为“已发布”“已通过构建”或“真实工作流已验收”。
