# Core 逐行阅读记录

日期：2026-09-23。范围：当前工作区的 `deepagent/core`。

完整读取了以下 200 个文本文件，共 27,777 行（含注释、空行、测试、提示词和 README）。二进制 `.DS_Store` 不计入。每个文件所有行均已覆盖；初始 30 批阅读后，额外 5 批补读工作区变化，并重新校验 SHA-256。哈希记录快照身份，不能单独证明理解；功能分析另见同目录 `2026-09-23-core-rewrite-design.md`。

没有修改下列源文件。工作区原有暂存和未提交代码均保留。

| 文件（相对 deepagent/core） | 已读行范围 | 作用 | SHA-256 |
| --- | --- | --- | --- |
| `adk/chat_model_agent.go` | 1–576 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `e52914701c1020634b4b3c5a0a7931b82aa5ce1d56af1778ba5d47a2b6ef5fd5` |
| `adk/chat_model_agent_test.go` | 1–541 | 测试：ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `8388a962ed4a9befe08b650454ee906d10594b7b30e491c6e800a7f57453de37` |
| `adk/deep_compat.go` | 1–254 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `99248e00ee26301590c2872008916f3868cec0c5c8429eae6d5e7c666dc06c84` |
| `adk/deep_compat_test.go` | 1–92 | 测试：ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `31e51869c0c316c084ac386db8e3622626d7e0b712340d652c94b0d57d51e942` |
| `adk/error_handling.go` | 1–251 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `ecc10ff7556b474fe472077c051c8bace996011ac18326f491d8f35400a89cf8` |
| `adk/error_handling_test.go` | 1–216 | 测试：ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `e6996cf42fc84c04abaecca7d3c4b1f7167c817bbf8e7e61ffc940618e33e982` |
| `adk/hitl.go` | 1–36 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `808b36b7257c044845474b2e64e5ecc3133cf8458934c5da97aef8a4c798ad23` |
| `adk/lead_agent.go` | 1–82 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `e58eaa379ffc57f90c0bf4e0b7b709e2d5d9d98fa0fdce96ab35dded1ec93b2d` |
| `adk/lead_agent_test.go` | 1–36 | 测试：ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `30d9968e0277b94358f1453dda6ff8c897305bb06ca44c41148104de4e2c69d9` |
| `adk/middleware_chain.go` | 1–123 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `cd5c854fae1a668deca7ce923b445fe534ac9a13ee0333bd5b79d0e70ed56fa3` |
| `adk/middleware_chain_test.go` | 1–116 | 测试：ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `abfe01818fd9c0a14829ae190bac811774f7ab8359995e9b7dccb2eeb64148db` |
| `adk/models.go` | 1–131 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `0293e84c5fb58adac030e640c432df2bdd56badae4169a3c86fab1c12aa10944` |
| `adk/prompt.go` | 1–564 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `b131e589f6e90e589fa20ee0b0581f49a8efcd26cb9e368b330ba6e69da05121` |
| `adk/prompt_test.go` | 1–287 | 测试：ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `9ff1e901bff397a8e1ccc3c0037c8be5e39de6b517dd3bec4f56d7af7869c18a` |
| `adk/subagents/subagents.go` | 1–8 | ADK 接口适配、手写主循环、模型工厂、提示词、TODO/子代理 | `a37a599f300f8f08bdc291cc5abbad95985696efa36dbbf660e5ac933af7569b` |
| `agent.go` | 1–231 | 公开配置、构造、运行 API 与 Graph 组装 | `1111576e1365c68baec08cdee279a07896b17996a96aaa6babf859dcd18b70ee` |
| `agentthread/callback_helpers.go` | 1–74 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `f38caf97aacc6e53f6596e2d9835e4f1651a7baf83aebf5a8abecb9e07931ae7` |
| `agentthread/context_contracts.go` | 1–88 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `54eaa45bdd67f5300b9f73a7f675570303f09f51e3b221266310fe67b3d34e16` |
| `agentthread/context_middleware.go` | 1–158 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `7e952e61a96c2d417f540c3744cffa28a925bbadebf6024008cbbb7897d964b6` |
| `agentthread/context_mng.go` | 1–499 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `ad37f4aacb5540a6328a65e88a85ff8582cd2c7936ae734b0298c0f3d7af05e3` |
| `agentthread/copy_helpers.go` | 1–54 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `e6692eaa5ad7b9d6f598ee5e52f60f80c44207800c0bd739744833b0360361c7` |
| `agentthread/event_callback.go` | 1–1 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `df5b0beb27e06055336a63568378fd673baaf4fd7507c6510ff36cd7c09482bd` |
| `agentthread/history_idempotency_test.go` | 1–113 | 测试：会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `7ff3cca29ca92a1ff1c34bb48317d56f31e0f104cac4ef5c94f745f79b7a7a9a` |
| `agentthread/model_callback_helpers.go` | 1–148 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `6730afb3fed93e11d5e5af3a0b8cb39e5d0b9aee19a1324b4bedae3ea126f34a` |
| `agentthread/model_event_barrier.go` | 1–21 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `437212eea0801e51974a9eac4e977ed3ebac525355eb263e4fa67f43056476c3` |
| `agentthread/options.go` | 1–60 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `707872ae2737fc497f50907db179e5d98405b5ac76948b2a1f6e0bfa51c4bff4` |
| `agentthread/redis_seq_generator.go` | 1–34 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `aa9f7a7245cc3c090e9e86367a6b08007823ff582a45bea2d3a07e872e4bff62` |
| `agentthread/rollout_gorm_store.go` | 1–192 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `8b3d53b96f216485d19c07f4a7692f77247317bd0c6d74d1bf9bddd033feef50` |
| `agentthread/run.go` | 1–321 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `3e61b77a0e9438b5c9ed70d95ede69d60dfe07d14052a9c4661f264b3c8201e0` |
| `agentthread/run_config.go` | 1–50 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `012519967cbc9105d02147cf50521aa48cc28e880b6e74357bb2a7b11476256f` |
| `agentthread/run_event_recorder.go` | 1–404 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `b4177931d1704af4a0e52cc1e3c13918905f6a7d0a4b1ea83b11be907be1d7fb` |
| `agentthread/run_event_recorder_test.go` | 1–16 | 测试：会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `2a9ac6defee56929c706131be3b1e69640c91afeb1f91c5a970b90202f361af1` |
| `agentthread/run_handle.go` | 1–61 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `14725ae0e20ff87b585fba6bbda17dfc170a84f3eb9b48a4df63b58628c46140` |
| `agentthread/run_start.go` | 1–33 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `7e4579da6085aac5eb5db715d6e05fec49c5da31811de95d080eedeb3b5264b7` |
| `agentthread/summary_compaction.go` | 1–62 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `1b04ebfee71906bf6f8cf3da8565d5d29e84ea00dd87b20d164141cddcc9299f` |
| `agentthread/summary_compaction_test.go` | 1–39 | 测试：会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `11224798dabf7b4a181d6138e27f5649a505ac8919622d8c2fb42376670db716` |
| `agentthread/thread.go` | 1–475 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `a7cce2403ffdd2f98b0a83d65e2155b96f13172e655487b5462eb199b04e9692` |
| `agentthread/thread_test.go` | 1–341 | 测试：会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `c36280df6ebcfa9c56976464c6bac16992a8f2551d9d8fdc9f63215475bcd7eb` |
| `agentthread/token_usage_tracker.go` | 1–263 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `e62192226fd612170b1e9777e2bc19b9ebfdeb36075da3f697bf688755934513` |
| `agentthread/tool_event_state.go` | 1–178 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `ad93ad90f64894a720d92a08bb0d56197ef0c33d3e12abbfcf012e0dcbeed250` |
| `agentthread/types.go` | 1–281 | 会话/Run 生命周期、输入、事件、历史、压缩、用量和存储适配 | `52dd3b368debb4104525c67f60a615b62db14b2a53fcc13f0ce8dd476a1aeb7c` |
| `backends/backend.go` | 1–220 | 文件/命令/工作区能力和存储后端 | `87d2abe3811207b1f9fbd98b52f150adaa5e2924733e4700e31c62def03b99af` |
| `backends/file_text.go` | 1–52 | 文件/命令/工作区能力和存储后端 | `0a22e6a2925513172d920906da08d3d16166e574a316f47ad66328fb88ffa323` |
| `backends/filesystem.go` | 1–797 | 文件/命令/工作区能力和存储后端 | `edb8e15fd56fb6a70ca11a28660fd6f882562f35ec527faa60253ec3439a8dff` |
| `backends/safe_execute.go` | 1–162 | 文件/命令/工作区能力和存储后端 | `0c0dc6752243e8cd5e1471d5a91b072b0cf287d2518d6ba87d5e08ba93da28b4` |
| `backends/shell_cancel_unix.go` | 1–26 | 文件/命令/工作区能力和存储后端 | `b787e179a5cec83e671a0d5bccb7e790962cefdb8fd65c70c30f3745b29b2a35` |
| `backends/store.go` | 1–287 | 文件/命令/工作区能力和存储后端 | `969ab50b39b3ce755fd91ab2311376a97c8e65e74e211bd66d0e500887604104` |
| `backends/store_backend.go` | 1–478 | 文件/命令/工作区能力和存储后端 | `b942d00e1585c21d49d7047be76fbd6b39398e995bacc762ca103d678793d07a` |
| `checkpoint/file.go` | 1–82 | 检查点存储与键验证 | `13e37c93b2aae7143ddab95c9a9c0f35bf9879d5ba49db835f628f1e6c4529f7` |
| `checkpoint/file_test.go` | 1–32 | 测试：检查点存储与键验证 | `0dfca2c591fbe7c1e28e564badf890abf0ed407567aef03accf60273a7210e6b` |
| `checkpoint/redis.go` | 1–72 | 检查点存储与键验证 | `9259ed9218d58d7b3b0df3053215375881b812416c02811a4c06d9dd43627a97` |
| `checkpoint/redis_test.go` | 1–39 | 测试：检查点存储与键验证 | `baf01b40b4e55664363cd7b81bf2263c0a5ea39ffca38b7f7e549a1abb6d5ac3` |
| `compact/compact.go` | 1–81 | 旧路径上下文压缩与重建 | `35fd9d747a1e62fff0ba2d0e7431ff6658eeb747e89d682c4f52447c809a22dc` |
| `compact/compact_test.go` | 1–45 | 测试：旧路径上下文压缩与重建 | `1911df8d1dd1bb2361988f6a2d765a89d02670c0cff672a950c4904ebf999cef` |
| `config.go` | 1–524 | 公开配置、构造、运行 API 与 Graph 组装 | `3fe59f323950c0e41cf0f9292645e5e88425b0bd7b4043a6fdc592645a585ead` |
| `constant/constant.go` | 1–13 | 常量与上下文键 | `9822d415543307db85ba9c7d17de44212a1e2ce9a66201586c965af1d4cbadd6` |
| `constant/default.go` | 1–13 | 常量与上下文键 | `69c6018d243abe8b27e366e378bb6b2c943ec50bf67c93f59c4f59d222e6b5ce` |
| `constant/graph.go` | 1–14 | 常量与上下文键 | `f60533775ca7327224172ee318011e667ec0015164f4ce260f92f11f99b8e2c6` |
| `constructor.go` | 1–177 | 公开配置、构造、运行 API 与 Graph 组装 | `993b78cbe5bf9c4b3b5ef08aae769d9cced28408f1e03474649f66acf3cbb7ea` |
| `constructor_test.go` | 1–254 | 测试：公开配置、构造、运行 API 与 Graph 组装 | `385ac5b8137d00d7c5e23d79b1f16cbcc64167b74d4346d2c07fcffea2aaf3fa` |
| `context.go` | 1–37 | 公开配置、构造、运行 API 与 Graph 组装 | `1e288991bbfdcf950f9f615a6fee43d6ff96d4d98417848002ba76b6c7a52cc2` |
| `engine/README.md` | 1–23 | 显式状态 Eino 执行图和有界子代理 | `b1b4f77d1a4143198ced8e697d8e8455b39282e80703095ca3abb05b0cf9e417` |
| `engine/agent.go` | 1–363 | 显式状态 Eino 执行图和有界子代理 | `f07372586dd3e30925c4632011f8092e3bf2a8a7d5499303b9f3da43415e74c4` |
| `engine/agentthread/compact_test.go` | 1–81 | 测试：另一套 Thread 生命周期、历史及检查点适配 | `6f0fac3367bbf651d53bdb70f4da168244d9f3f7e7c531231fdf6eb4d47b6748` |
| `engine/agentthread/thread.go` | 1–569 | 另一套 Thread 生命周期、历史及检查点适配 | `5d3029d57427a249c680588517d4a02ab65daad5ebc38bf877ab6115a20f79d8` |
| `engine/agentthread/thread_test.go` | 1–464 | 测试：另一套 Thread 生命周期、历史及检查点适配 | `0c134557f6e282278fe060c75a98328bc3541df917df236ad24257dd1562609f` |
| `engine/subagent.go` | 1–76 | 显式状态 Eino 执行图和有界子代理 | `56f9cf67a33824c2afa1e4ee730968cbf2babb70a3e47209fefb6161cb364e28` |
| `engine/subagent_test.go` | 1–54 | 测试：显式状态 Eino 执行图和有界子代理 | `508db85a84ea2e46d16d48c3a9abe9a30d1f7d08c72cc7ca806f1de00d019767` |
| `graph/branch.go` | 1–35 | 流合并、工具参数收集和工具执行节点 | `a6034a7c23628d557b5be7d22e84aca41d8fccbe5ef198cf019256891a044352` |
| `graph/message.go` | 1–90 | 流合并、工具参数收集和工具执行节点 | `6e8c2a2af6fa2d46143de583e7254b8fc543e8ae987fa20f1e030737a5e865d5` |
| `graph/stream_tool_executor.go` | 1–725 | 流合并、工具参数收集和工具执行节点 | `c9853d0c986bc2441713474bc226944ecd033cbbe2f645f73b18124068225954` |
| `graph/tool_call_collector.go` | 1–167 | 流合并、工具参数收集和工具执行节点 | `55eda4fc13254a7c2f77e3f49d63f785b4bf18f0ec99de0e6ca1b5e1821ae82e` |
| `graph/tool_call_collector_test.go` | 1–28 | 测试：流合并、工具参数收集和工具执行节点 | `2e76ebd300236ac11452c0893ce796d96034b0593e161353c011e053d5247177` |
| `graph_builder.go` | 1–479 | 公开配置、构造、运行 API 与 Graph 组装 | `d70e86ad84b423012831bc58e2b2be0e27247e0429d26db9bd7dfd22c287edfa` |
| `hooks/hooks.go` | 1–83 | 模型/工具/Agent 生命周期扩展类型 | `db8b8c784342ebdcbd2e905f19ed84a50019f3efaf7948109b5e2f5c0a672da2` |
| `internal/toolerrors/errors.go` | 1–29 | 工具错误转换 | `4cb28f3ece50124e213a8878a4e0b4154c3f08dc3f55cd44003b475f587807e4` |
| `max_model_calls.go` | 1–134 | 公开配置、构造、运行 API 与 Graph 组装 | `99c32a497ecb7fcf17039892e22151814a7f0ac3f3f7e43a889d1cc379c2e6fe` |
| `mcp/mcp.go` | 1–300 | HTTP MCP 发现、会话、调用和清理 | `78f6eca4cbb529c16099d1df831fb092a1036ca8ea39105b834486c8e62cbb9b` |
| `mcp/mcp_test.go` | 1–70 | 测试：HTTP MCP 发现、会话、调用和清理 | `0b76dbd97ecccd5529171977269f9f558803eb8cd157d8bbb319221612790cf6` |
| `memory/agent/dream_memory.go` | 1–43 | 画像/事实抽取、合并、渲染和注入 | `333134db3e7d57d348f6fac8578286059b8952124ccb623e78a6403cdfbd46c2` |
| `memory/agent/memory.go` | 1–44 | 画像/事实抽取、合并、渲染和注入 | `5914d631e135b37978feb43dae616d80f9aca01e8f40aafb828be887ed426b80` |
| `memory/agent/memory_render.go` | 1–164 | 画像/事实抽取、合并、渲染和注入 | `539157b7a3143da69c85899bac31e2b70bc476e38781a7cec9e5401529f03a87` |
| `memory/agent/memory_test.go` | 1–246 | 测试：画像/事实抽取、合并、渲染和注入 | `2887c8353a836a97323ba9cdea4400e5ca3a6af64a1ac73906d31a4037181e8b` |
| `memory/agent/memory_update_prompt.go` | 1–200 | 画像/事实抽取、合并、渲染和注入 | `b3dc07803a3d4661646d1c8737a6c4b657fe792a810d5f64907aa5f2c2b25c96` |
| `memory/agent/memory_updater.go` | 1–275 | 画像/事实抽取、合并、渲染和注入 | `ae172b98647d7e16293ff5b43d511e50aaf9fd175ad4eb69b76084db85b50439` |
| `memory/agent/memory_updater_test.go` | 1–481 | 测试：画像/事实抽取、合并、渲染和注入 | `8ba0219d8df5c89fe1f31cca277a2119d2bf4c40eeb6427bd3a8e0077a20bb25` |
| `memory/autodream/autodream_test.go` | 1–74 | 测试：自动整理触发条件和锁 | `1cf5ebe0d7eb8271dfa272ca35d4c0949db148ded11af97684af590d8076bfe9` |
| `memory/autodream/lock.go` | 1–127 | 自动整理触发条件和锁 | `2561906c5514562b7967f4a464d5070e2e93a2f87fbf3cf383a1117ed0688724` |
| `memory/autodream/prompt.go` | 1–34 | 自动整理触发条件和锁 | `a9ba0342fb49c99ef90d1e0c5d5a8df5a9218fa01fa119865722e4948759e6f9` |
| `memory/autodream/sessions.go` | 1–43 | 自动整理触发条件和锁 | `ebbda776131dda85e5d204757238dc104da7fec12f0d2eb595e2d317f2d38c8b` |
| `memory/autodream/types.go` | 1–18 | 自动整理触发条件和锁 | `270bfcd7d83ccd1927301d36baeb49f2fa02d3fa9a3f74d92ccc2c9b67969697` |
| `memory/consolidator.go` | 1–74 | 长期记忆流水线、抽取、合并和租约 | `f666aa9ebab39cb8fe58a5203f724f7da00687adadb10f3f941c22edab632ce4` |
| `memory/consolidator_test.go` | 1–45 | 测试：长期记忆流水线、抽取、合并和租约 | `ffe38ec0667ad01430b86d7d934efc36888d7091787615f5b52ac3e9845967c8` |
| `memory/durable.go` | 1–161 | 长期记忆流水线、抽取、合并和租约 | `30d1cbf174b49d091fd98e13ddec79991511d7268ddb10d68cbe6a1468ebc73d` |
| `memory/memory.go` | 1–240 | 长期记忆流水线、抽取、合并和租约 | `8fd5f66648901372e24bc4fcd46696f3c3b7d7ba671d316f677475c2442ec0b1` |
| `memory/memory_test.go` | 1–184 | 测试：长期记忆流水线、抽取、合并和租约 | `1f3b1e2c7ee619db6c70a511106477f393e1754f5ab228a8e393c0c675968e6f` |
| `memory/store/agent_name.go` | 1–23 | 结构化画像模型、读写和路径 | `f863bb5bdbc78ae9dfd021cfc65ede8d0b99bd6c4ff6c6cf84c8dd03d2cead9b` |
| `memory/store/data.go` | 1–117 | 结构化画像模型、读写和路径 | `cdfb1af600d2bf482ac7a17fa1868dbf071903a1c1e3399421c2fad3561f4c1b` |
| `memory/store/store.go` | 1–74 | 结构化画像模型、读写和路径 | `442d148621a077c05da8bc0a667a1436dd5c4bc43e45b0dbe9311694292ba024` |
| `memory/store/store_test.go` | 1–208 | 测试：结构化画像模型、读写和路径 | `0ffaf95c95168807b5257ee0193d32c239cfe990e246d7023dd560ea0701466d` |
| `middlewares/adk/agent_state.go` | 1–61 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `1f5695642d04ce343be7a4bff2bac5686e18f6d3272b6c845ca68399f3cc7b5d` |
| `middlewares/adk/clarification.go` | 1–164 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `fab95124f133172dd868e2af2f991a0ad01708c7716d503fcbcb4ad49fb4df69` |
| `middlewares/adk/clarification_test.go` | 1–143 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `2806c2aaa299a2fc74e1989a224b9b4143b68dfe4a1fb8f3b3154ec846d63e44` |
| `middlewares/adk/deferred_tools.go` | 1–61 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `b5309cf52a4fb95a03dd55b42ee38f4a8480f8473876f087431ac09d20a6062e` |
| `middlewares/adk/hitl.go` | 1–83 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `6f1ff117f7c99e0c39c10220ba3e10804f178b574157612d63a36a9c891d932c` |
| `middlewares/adk/hitl_test.go` | 1–93 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `0c642084198cb1118bab50705322d2288dbb0d8d1efe004ed03f9931f701e87c` |
| `middlewares/adk/loop_detection.go` | 1–110 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `8020512731bd58acd882022b07655ea3a9be584b62e6e7a19f8b943e7cfc382e` |
| `middlewares/adk/loop_detection_test.go` | 1–69 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `888195db21feeb7e5e1fb4246680913ce65f107d48107b62fae9262996ccb17a` |
| `middlewares/adk/memory.go` | 1–61 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `8e77f2f7080a5768d7004727565acf78b61baee000fe133ac5ffd49ca8c5289b` |
| `middlewares/adk/messages_log.go` | 1–212 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `f2d3dc3af1804021c87c80b5112fe28265bad41b8520b7e8fd3943542685462a` |
| `middlewares/adk/messages_log_test.go` | 1–170 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `43325bb99f9684e84c0c6d3dac1806e7f0294c380f1745607b9800bd02f17a82` |
| `middlewares/adk/plan_reminder.go` | 1–50 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `590785d4e884beb910c1362fad0f2c2faecc7d592b762176ad5292670eb93e09` |
| `middlewares/adk/plan_reminder_test.go` | 1–98 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `fe98d9871ef4d60fc58a4a5fe86612a85e4af1488da990dc577a37b99263b0f0` |
| `middlewares/adk/sandbox.go` | 1–58 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `2ce81da37ce1ee63bae4a886e3807bb9c2d968edff87c351a2d8211c1fd3ca42` |
| `middlewares/adk/subagent_limit.go` | 1–73 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `14ebc13cf69abc6a99074eba30c89bbc1ddcad1b0ee69c0bb387f322c6f56f97` |
| `middlewares/adk/subagent_limit_test.go` | 1–71 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `c10067130f996a736159d3463c7cf1fec4931a414f10db5f2f592762e704606f` |
| `middlewares/adk/summarization.go` | 1–47 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `b9712e4bd9ae51d8950bd30932afcb0149242fea03d05e279b061567548d9400` |
| `middlewares/adk/todo.go` | 1–21 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `cbbff76361d1bd0f41bcf898721113eba18c488d8e1146fa409ca96d800e289b` |
| `middlewares/adk/todo_reminder.go` | 1–115 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `d5ece9d6c1f8edbe22396f5e45eaf95a94b990cf215db96049e88cdc17667a88` |
| `middlewares/adk/todo_reminder_test.go` | 1–129 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `735530621b652856d7779c0fcad12726ccb538fd2e8f6220f6e9f43d7a80b71d` |
| `middlewares/adk/token_usage.go` | 1–124 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `2725527d7ab01131b27e888f7cf2a0265f7bbd2cd2495a7ea23b3cfee2b94209` |
| `middlewares/adk/tool_error.go` | 1–42 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `9927eec029a1ece13cfc648c70b3d1f2d30e2a06a6ae7d44300b8693172f3e8c` |
| `middlewares/adk/tool_observability.go` | 1–52 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `a0f00d4f96dec07fd9b2ef3e38d8009cf18cd8c4b83b378c0852b4f182a04535` |
| `middlewares/adk/tool_observability_test.go` | 1–84 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `0685b12df1331fc6921472f20aa26926e4b4fab6c4d881f017906f0257af8575` |
| `middlewares/adk/trace.go` | 1–164 | ADK 生命周期策略、观测、审批、压缩和提示注入 | `3d6bcd00df2bca916f741e473e128143548772624754b66e68b196b09644959e` |
| `middlewares/adk/trace_test.go` | 1–252 | 测试：ADK 生命周期策略、观测、审批、压缩和提示注入 | `0525582ee000983182c5bf2a22ec5f8f5e95585013245cd3769436deecb88bd4` |
| `middlewares/base_prompt.go` | 1–32 | Graph 扩展契约与具体能力组装 | `5a98da76b57b5727af37c356f2c84858d0463893b9113680f1624c7acaf74375` |
| `middlewares/baseprompt/baseprompt.go` | 1–7 | Graph 扩展契约与具体能力组装 | `6a47dae43fe14d55783016ac99ef1950668e38fe84e7548c9070820bfa917ca5` |
| `middlewares/contextmanager/contextmanager.go` | 1–5 | Graph 扩展契约与具体能力组装 | `3df19c6da58abfa6279a4058e6a321efbd8997e741005aa6f14d8ba5ea9276b7` |
| `middlewares/execute/classifier.go` | 1–372 | Graph 扩展契约与具体能力组装 | `fa26e2bffa5107f037816062879ba17d3205a39a2a4c79b8250d78cfd56a8920` |
| `middlewares/execute/output.go` | 1–13 | Graph 扩展契约与具体能力组装 | `dd56c32a9115e03fc95a9a48c3661fd257b4fae89131cd33da4ff232309c38d0` |
| `middlewares/execute/request.go` | 1–123 | Graph 扩展契约与具体能力组装 | `25d9b35c03e1aa3ed156a41d403865ffcede0f8ec8a5f2beeece712af26bfc85` |
| `middlewares/execute/types.go` | 1–47 | Graph 扩展契约与具体能力组装 | `32cdfe0e61db5089ed2c67bdb37097fe47c5eb64674e33211f0a3d3e18ea32fb` |
| `middlewares/filesystem/filesystem.go` | 1–200 | Graph 扩展契约与具体能力组装 | `058b9e3761b9d060d6bbd8be816eabc6cd55ab9c92554f5a413a1daab29fc528` |
| `middlewares/filesystem/filesystem_test.go` | 1–182 | 测试：Graph 扩展契约与具体能力组装 | `8b9ad908ff44f19060a542ff793f4c662fc9802a148fb5b006a9120e83f5f4d9` |
| `middlewares/filesystem/shell_jobs.go` | 1–223 | Graph 扩展契约与具体能力组装 | `9c28b7631a5ff8ea43923c2399d14094f1045965dd064f12f9287fbb2f4820e8` |
| `middlewares/filesystem/workspace_tools.go` | 1–206 | Graph 扩展契约与具体能力组装 | `684096e93ab8a8aa707d84352286ad1766d087acbeba46b0d4a109d8a9463225` |
| `middlewares/middleware.go` | 1–174 | Graph 扩展契约与具体能力组装 | `4aa24fa9fb0db51ebfa9492d402df0057d013743e579ccb2d556dd19668b2193` |
| `middlewares/patchtoolcalls/patchtoolcalls.go` | 1–53 | Graph 扩展契约与具体能力组装 | `c7ea45a756521e0a0b518e1457fac55f9263781f38cf9b5625705141925ed6b3` |
| `middlewares/plan/plan.go` | 1–90 | Graph 扩展契约与具体能力组装 | `c6938fe8a5e94aa7767ab5ceb5e4222083b923e729c12f5d64735987e061dfa1` |
| `middlewares/plan/plan_test.go` | 1–40 | 测试：Graph 扩展契约与具体能力组装 | `77ebce32762024608120c0c099d81734cf23ab8797dba02ed7793b10a73b9e4d` |
| `middlewares/planmode/planmode.go` | 1–12 | Graph 扩展契约与具体能力组装 | `f1726067cb14c4d6224cec9593d5e9e226c4c63fa4234311795dfcacb2c2678a` |
| `middlewares/repairjson/repairjson.go` | 1–56 | Graph 扩展契约与具体能力组装 | `dd9a4005e863707d91c11cfeb2d41930baf712d7e9a65d52c4ad266ea87242cf` |
| `middlewares/repairjson/repairjson_test.go` | 1–20 | 测试：Graph 扩展契约与具体能力组装 | `3767da03cb5410a0b0aeda8f571184d51375dc667e2328b67064c715f2cd8bf1` |
| `middlewares/simple_context_manager.go` | 1–113 | Graph 扩展契约与具体能力组装 | `8dc10dd9d727c45c222f466dc6318a4e83868f9529be9771072b8bfbeea83db4` |
| `middlewares/skill/loader.go` | 1–153 | Graph 扩展契约与具体能力组装 | `1dfeb617fdf9255045c5adf33d6ccf28021c80a5806ed098e454266b75bdbc50` |
| `middlewares/skill/loader_test.go` | 1–29 | 测试：Graph 扩展契约与具体能力组装 | `556ead2cf15f02618a0575c8fc290be9723fc73a29bfb98ce35e19e2480a267c` |
| `middlewares/skill/skill.go` | 1–123 | Graph 扩展契约与具体能力组装 | `a9bf3523861c530ba5a4906764a0230955259bb9a4ff0c86a95037ce8a1499e8` |
| `middlewares/skill/skill_test.go` | 1–37 | 测试：Graph 扩展契约与具体能力组装 | `c098ff2e248fc059cd08e062a15b344f7cd7c4da7a31b6c9465c9f9945284a2b` |
| `middlewares/subagent/subagent.go` | 1–55 | Graph 扩展契约与具体能力组装 | `4223b31131f11e59c8d6c5728bd045f22e444e030fc7ca5e1142fdca5c2ad405` |
| `middlewares/web/web.go` | 1–206 | Graph 扩展契约与具体能力组装 | `12505d6f4e1b0289334ad2c3ff93c6109794697a4b4d2be42da8ddb687a56570` |
| `middlewares/web/web_test.go` | 1–43 | 测试：Graph 扩展契约与具体能力组装 | `a0605d07e413dce1a28043f68229a238cdde7a8dd6ed69d338053f3663e1bec0` |
| `modelhub/models.go` | 1–72 | 统一模型提供方构造 | `8bcaf03bb5af502fa9f7c962d4e4eb94a709cd90f129b10f510d2a3ee2b5a2b2` |
| `modelhub/models_test.go` | 1–20 | 测试：统一模型提供方构造 | `9d9617a2b9ee5f6ea52ddce195de1a183f19b77eb1b579894b15df44cb363a04` |
| `run_options.go` | 1–64 | 公开配置、构造、运行 API 与 Graph 组装 | `356b3bfa49f7e7a0b9bf46640b0c6de6fc35cd3f44de6c6cbad3337fe3d66743` |
| `runtimecontext/context.go` | 1–102 | 会话、sandbox、权限和回滚保护上下文 | `a0c1ff7d5b6ad2c11e5698813261076f32574167cdafda054575f0ccaeabf5ed` |
| `subagent_factory.go` | 1–118 | 公开配置、构造、运行 API 与 Graph 组装 | `fecb820d15278bdbceffcf66229e208d73e28f6e839161b7b3434143cbe8dc5f` |
| `subagent_validation.go` | 1–22 | 公开配置、构造、运行 API 与 Graph 组装 | `44536896ff14513322e33dfeb69442abc1652687373c4d9600fb3d02d3110c62` |
| `tools/adk/apply_patch.go` | 1–209 | 旧 ADK 工具、虚拟路径和能力测试 | `5991be64fdca433922225e8b66394320bbf2a6903c1e461a276c0a4e489df2df` |
| `tools/adk/auto_dream.go` | 1–121 | 旧 ADK 工具、虚拟路径和能力测试 | `740c6389b16870f7107a87d679ee27785c16ee46e930837d86b29f79d6c5e298` |
| `tools/adk/auto_dream_test.go` | 1–41 | 测试：旧 ADK 工具、虚拟路径和能力测试 | `048c953c9b01ec386e82a65220d33438fed66a8300b56d8962cf089875a8c4e8` |
| `tools/adk/await_shell.go` | 1–85 | 旧 ADK 工具、虚拟路径和能力测试 | `8d16e6ba3153a532ad11ac62d7435768efe7aa709b573f4b192c1cb2c3bb9621` |
| `tools/adk/clarification.go` | 1–28 | 旧 ADK 工具、虚拟路径和能力测试 | `fcbb21c210a1f6586198c303fb11a9b033ccd124ff33e7b8a98ee9e52008974b` |
| `tools/adk/delete_file.go` | 1–81 | 旧 ADK 工具、虚拟路径和能力测试 | `59d57e4f230a10efadd5b712f1f398d538264f0735582d955a21c7ec5a4d3c99` |
| `tools/adk/edit_file.go` | 1–83 | 旧 ADK 工具、虚拟路径和能力测试 | `132980ec257b9f328b9a84ce868812519a4ba2a1042122038c40a5542b26ad42` |
| `tools/adk/execute.go` | 1–64 | 旧 ADK 工具、虚拟路径和能力测试 | `26a35470f8fd8fc6f0ea0759d050cc6ca0e6c96202750668127aba1826d7c2b5` |
| `tools/adk/glob.go` | 1–87 | 旧 ADK 工具、虚拟路径和能力测试 | `f01a420679f6c8e7b172832cad2b88e1d4a0f8fdb78640abcd87e3dc24600a44` |
| `tools/adk/grep.go` | 1–274 | 旧 ADK 工具、虚拟路径和能力测试 | `50299314cae322a616e9cb2b2934a48ee89e4ed23e3e24298daa946dec15fd3d` |
| `tools/adk/ls.go` | 1–56 | 旧 ADK 工具、虚拟路径和能力测试 | `3bf4bf484c12e1856177e5e248c8867c6010ead6dc00e96711eb9b9b0cb61089` |
| `tools/adk/path.go` | 1–61 | 旧 ADK 工具、虚拟路径和能力测试 | `871f275fffe67d4bfbca4fa329c369be87a6687a6b815b88babe0788504c4686` |
| `tools/adk/read_file.go` | 1–86 | 旧 ADK 工具、虚拟路径和能力测试 | `990a1d7c911ca3b0d46890beb8eb5aad40ac869560f367b3004b1818db4be349` |
| `tools/adk/read_lints.go` | 1–113 | 旧 ADK 工具、虚拟路径和能力测试 | `8fb0e0d9d89f93f808ab9b8345b00024bcfe883ce71fefb40c693f2bfa285c53` |
| `tools/adk/rg.go` | 1–158 | 旧 ADK 工具、虚拟路径和能力测试 | `7e02ab6fd94c15e7e99d8f86251cc2a63a0cad3ce1cb5434721b9c62b7e55268` |
| `tools/adk/sandbox_helper.go` | 1–73 | 旧 ADK 工具、虚拟路径和能力测试 | `2a66a8f231688f655868b25ecd69d87ad5b429cd424701e1cf7452e3d8655fb7` |
| `tools/adk/semantic_search.go` | 1–128 | 旧 ADK 工具、虚拟路径和能力测试 | `2174d8603c183fdaf3adb3fb6ab3a58f453b5d5dcfcc1a00d910cdcb545200a8` |
| `tools/adk/shell.go` | 1–126 | 旧 ADK 工具、虚拟路径和能力测试 | `467b7bf8b6380608507ed2e83bd431b9262e3762745a0ea0a85d6a951d4c2a19` |
| `tools/adk/shell_jobs.go` | 1–94 | 旧 ADK 工具、虚拟路径和能力测试 | `60acdb8b813f98c2ec873215f9578197ecde26fb817491343f6e4fe0828b2259` |
| `tools/adk/tools.go` | 1–59 | 旧 ADK 工具、虚拟路径和能力测试 | `178c8159fe9b6bf5b2168477016ae411948a36aab669c8a8744cbc2fb5bbf48f` |
| `tools/adk/tools_test.go` | 1–713 | 测试：旧 ADK 工具、虚拟路径和能力测试 | `15a481695b5749f869acbd8cf6f154e609d30788a015fcaca75943b9b6ee09d6` |
| `tools/adk/virtualpath.go` | 1–120 | 旧 ADK 工具、虚拟路径和能力测试 | `7575c716475db546b403b6e72371d1b79783089e70f09922ba29fb8b6bfd1ada` |
| `tools/adk/virtualpath_test.go` | 1–47 | 测试：旧 ADK 工具、虚拟路径和能力测试 | `d4a14011cb65f54b59879667ff902e3b2fd6259bb1e5d73475919d9a6a228221` |
| `tools/adk/web_search.go` | 1–127 | 旧 ADK 工具、虚拟路径和能力测试 | `5b1e1f414e9c9256b95e1fe447e52411ff122a74ac6e649811af4fd8d83223a4` |
| `tools/adk/web_search_test.go` | 1–148 | 测试：旧 ADK 工具、虚拟路径和能力测试 | `2e93514504c756d7b15a4ee8b09aed49d7cd126de789d05ff6c4ae7fc05203fb` |
| `tools/adk/write_file.go` | 1–51 | 旧 ADK 工具、虚拟路径和能力测试 | `7bd9ec67c56229d4dcb5532b5ab93c2f860517bbcb0a2ba55ffea93dce94e0c8` |
| `tools/delete_file.go` | 1–29 | 工具定义、策略、问答及新增文件工具 | `9980987475abf5e5d9788864bdfa5deaa66e8956d7c4f51d57f26642d110cf6a` |
| `tools/edit_file.go` | 1–36 | 工具定义、策略、问答及新增文件工具 | `28aba5e40fcc7e8ec6d9ec68df737a67e6199a0b9eb30d02f8e8cd62820b8760` |
| `tools/filesystem/filesystem.go` | 1–320 | 独立 rooted 文件工具、审批元信息和流式命令 | `9734ed72bf0cc037a1cc0fc707b0df3625b87dee6989e2e5755fd01a2a223ba6` |
| `tools/filesystem/filesystem_test.go` | 1–90 | 测试：独立 rooted 文件工具、审批元信息和流式命令 | `6eaff24fcdbda7e638be5dba7ce4f51d4453f565b28a028ab4632bf40baddd59` |
| `tools/follow_up.go` | 1–56 | 工具定义、策略、问答及新增文件工具 | `9abb5d08043d85d5e8b6c40b1b522f30fad364a7bf49e62c9d78fcc259388ff1` |
| `tools/list_files.go` | 1–31 | 工具定义、策略、问答及新增文件工具 | `3d0cfd9da4d177b208105433e2d6458fcbe4f14a21569030efdf4d99ec909814` |
| `tools/policy.go` | 1–42 | 工具定义、策略、问答及新增文件工具 | `6ca72ff666a2ed9744de00a491a7da09f6032e10958f40667665e9b54024305a` |
| `tools/read_file.go` | 1–37 | 工具定义、策略、问答及新增文件工具 | `7ea1520b4bce17258305d2f44700065527df6e3910a04c44f7e601790c1c87d0` |
| `tools/read_lints.go` | 1–55 | 工具定义、策略、问答及新增文件工具 | `43f40acecf36c1f8db0db0a8c83645a971115bca2d9a3a28ba03786ef687b1e4` |
| `tools/search_files.go` | 1–37 | 工具定义、策略、问答及新增文件工具 | `07e7eea170658ab658be15f8e3842bcea72c87b9750f1d905861aca76f7c05aa` |
| `tools/semantic_search.go` | 1–125 | 工具定义、策略、问答及新增文件工具 | `b805e5f0576fab801b752b3990cdbf587c925c82780c810a5cac8cbfbaba7d37` |
| `tools/tool_backend.go` | 1–35 | 工具定义、策略、问答及新增文件工具 | `d5dabdb64dfc8e61e585b2a471a1b6896b95ffe4c56282ecde5989ad6676a0e5` |
| `tools/tools.go` | 1–163 | 工具定义、策略、问答及新增文件工具 | `dbd7f76019ad3d5042efd2dcd526aff1db052f56a57f6e1102818dc3a3ab3e1a` |
| `tools/write_file.go` | 1–37 | 工具定义、策略、问答及新增文件工具 | `66840efc0e063a69408f627bd2234abf76eecb4292823850e70ab5c87958ffa8` |
| `types/graph_state.go` | 1–149 | Graph 组件状态和持久化协议 | `663eeb1c196f2811d85800c2f4bf69ed2cda8c233efdf150d64de2f3b0d64547` |
| `types/graph_state_test.go` | 1–123 | 测试：Graph 组件状态和持久化协议 | `f9b537e87d68d5825153d61f0f3e1360ca574163adab4a26126bf4d063b209a7` |

## 外部契约补读

以下文件完整读取，但不包含在上面的 core 统计里：

- `deepagent/thread/thread.go`：2298 行。
- `deepagent/thread/protocol.go`：91 行。
- `deepagent/threadhost/runtime.go`：223 行。
- `deepagent/threadhost/threadhost.go`：992 行。
- `deepagent/worker/app.go`：89 行。
- `deepagent/manager/client.go`：22 行。

## 当前验证状态

- `go test ./deepagent/core/...`：失败。`core/tools` 中 tools.go 与 follow_up.go/policy.go 重复声明，导致依赖它的包编译失败。
- `core/tools/adk` 的本地 HTTP 测试还遇到当前执行环境禁止端口监听；构建缓存清理也被文件权限限制。这些环境失败与源代码重复声明分开记录。
- 其他显示通过的包中包含 Go 缓存结果；没有把它们描述成本次全新运行通过。
- 未修改代码，未进行真实模型/API/MySQL/Redis/Web 端到端验证。
