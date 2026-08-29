# FlexConnect 全仓设计与实现对照审计

审计日期：2026-08-29  
FlexConnect 基线：`ba7becf`（`master`，相对 `origin/master` ahead 1）  
Tailscale 参考基线：`33af330c8ed8a1cb02530eaf70d9179f9656db2c`（2026-08-28）

> 1.3.0 修复说明：本文主体保留基线审计时的原始事实和行号。文末修复矩阵记录
> `codex/flexconnect-1.3.0-hardening` 对每项发现的处理及自动化证据；它不构成真实
> AnyConnect、真实主机网络或独立安全扫描验收。

## 1. 结论

FlexConnect 已经具备与 Tailscale 客户端形态相似的基本分层：特权 daemon、基于本地 IPC 的类型化客户端、CLI/托盘、事件流、路由/DNS 抽象和后端接口。但当前相似主要停留在进程形态和目录职责，尚未继承 Tailscale 在连接所有权、状态串行化、权限模型、网络配置事务、健康追踪和退出清理方面的关键约束。

本次确认 30 项问题：

| 等级 | 数量 | 含义 |
| --- | ---: | --- |
| P0 | 7 | 可直接破坏认证安全、连接所有权、主机网络状态或 daemon 稳定性 |
| P1 | 12 | 生产环境下有明确失败路径，会造成错误状态、权限过宽、永久阻塞或网络配置污染 |
| P2 | 8 | 可靠性、兼容性、可观测性和发布工程存在明显缺口 |
| P3 | 3 | API/协议细节和文档一致性问题 |

当前不应将仓库描述为“生产级跨平台 AnyConnect 客户端”。最先需要修复的不是功能数量，而是 TLS 信任、连接生命周期、网络配置事务、并发所有权和本地控制面授权。

## 2. 审计方法与边界

本次审计按下列控制路径逐项对照：

1. daemon 启动、IPC 建立、权限和健康握手；
2. profile/secret 的创建、更新、删除、切换和持久化；
3. AnyConnect 认证、CSTP/DTLS 建链、TUN 建立和关闭；
4. 静态路由、动态 split DNS 路由、DNS 设置和恢复；
5. 连接事件、自动重连、网络修复、流量采样和诊断；
6. SOCKS5、CLI、托盘、watch 流和本地 API；
7. 平台实现、退出清理、升级、构建、测试和发布。

Tailscale 仅作为成熟本地 VPN 客户端控制面的参考，不要求 FlexConnect 实现 Tailscale 的 mesh 控制面、DERP、节点密钥或 tailnet 功能。对照重点是两者共同面对的客户端工程问题：状态所有权、网络事务、权限、健康状态、事件一致性和退出清理。

结论分为两类：

- “已确认错误”表示可由当前源码直接推出，或存在确定的错误分支；
- “设计缺口”表示实现缺少生产系统必须具备的约束，具体故障仍依赖平台或运行时条件。

## 3. 架构对照

| 关注点 | FlexConnect | Tailscale 参考 | 判断 |
| --- | --- | --- | --- |
| 特权进程 | `flexconnectd` | `tailscaled` | 方向正确 |
| 本地控制面 | HTTP over Unix socket / named pipe | LocalAPI over local transport | 方向正确，但授权与请求校验不足 |
| 类型化客户端 | `client/local` | `client/local` | 方向正确 |
| 状态协调 | `internal/appd.Service` | `ipnlocal.LocalBackend` + profile/prefs/engine/router/DNS/health | FlexConnect 职责过度集中，缺少单一事件所有者 |
| VPN 后端 | `vpn.Backend` | engine abstraction | 接口缺少 `Close`、健康、能力和 attempt 身份 |
| 路由/DNS | `internal/osnet` | `wgengine/router`、`net/dns`、`net/netmon` | 抽象存在，但事务和平台实现不足 |
| 事件 | `/v1/watch` NDJSON | state bus/watch notifications | FlexConnect 无序号、无 gap、慢消费者会永久丢状态 |
| 身份与授权 | 依赖 socket/pipe ACL | 连接身份、operator、读写权限 | Windows 权限明显过宽 |
| 健康 | `/v1/health` 固定 `ok` | health tracker 与组件问题 | FlexConnect 只证明 HTTP 活着 |
| 退出清理 | HTTP shutdown | engine/router/DNS/watch 有序关闭 | FlexConnect 未关闭 VPN 和网络状态 |

## 4. P0：必须先修复

### FC-001 TLS 证书验证被全局关闭

**类型：已确认错误；安全。**

- `internal/anyconnect/base/config.go:27` 将 `InsecureSkipVerify` 固定为 `true`。
- `internal/anyconnect/auth/auth.go:147` 将该值送入认证 TLS 配置。
- `internal/netcheck/netcheck.go:415` 与 `internal/anyconnect/tunnel/dtls.go:50` 也直接关闭验证。
- 该通道承载用户名、密码、认证响应和 VPN 会话材料，任意能影响 DNS、网关或链路的攻击者均可冒充服务器。

Tailscale 的 `net/tlsdial/tlsdial.go` 即使需要设置 `InsecureSkipVerify`，也会安装显式的 `VerifyConnection`，并对意外的不安全配置直接 panic。FlexConnect 当前没有等价的证书链、主机名、pin 或用户确认机制。

**修复要求：** 默认使用系统信任库并校验主机名；若 AnyConnect 部署需要自签证书，应提供显式 CA 导入或经过展示指纹的持久化信任决策，不能保留静默跳过验证的路径。认证、CSTP、DTLS 和 netcheck 必须共用同一信任策略。

### FC-002 随机数失败被忽略

**类型：已确认错误；安全/正确性。**

- `internal/anyconnect/tunnel/tunnel.go:39` 忽略 `MakeMasterSecret` 的错误；失败时会继续使用不完整的密钥材料。
- `internal/types/types.go:292` 忽略 profile ID 的 `rand.Read` 错误；失败时可能生成全零或重复 ID。

**修复要求：** 将随机数失败提升为终止错误；ID 生成函数返回 `(string, error)`；在持久化前强制检查唯一性。不得增加伪随机回退。

### FC-003 删除活动 profile 不会断开实际 VPN

**类型：已确认错误；生命周期。**

`internal/appd/daemon.go:494` 的 `DeleteProfile` 在删除当前连接 profile 时会清空服务状态并停止代理，但没有调用 `backend.Disconnect`。结果可能是：TUN、路由和 DNS 仍然活动，控制面却已忘记该连接，之后无法可靠地通过 profile 身份关闭或诊断它。

**修复要求：** 删除活动 profile 必须是显式事务：先进入 disconnecting，取消/关闭对应 attempt，等待网络资源清理成功，再删除 secret 和 profile；任一步失败都应保持可恢复且可观察的状态。

### FC-004 Connecting 状态不可取消，迟到结果可复活连接

**类型：已确认错误；生命周期/并发。**

- `internal/appd/daemon.go:618` 以 `connectedID != ""` 判断是否需要 disconnect；Connecting 阶段通常为空，因此 `down` 会成功返回但不取消建链。
- `internal/vpn/anyconnect/backend.go:39` 在整个 `Connect` 调用期间持有 backend mutex；即使调用 `Disconnect`，也无法抢占当前建链。
- Service 没有 attempt/generation ID。旧 Connect 的迟到成功可以在用户断开或切换 profile 后重新把状态写成 Connected。
- backend 的 `connected` 事件没有携带 profile/attempt 身份；appd 使用当时的 `currentID` 归属事件，可能把 A 的连接记到 B。

Tailscale 的 LocalBackend/engine 路径强调单一状态所有者和显式重配置。FlexConnect 需要把“用户意图”和“某次连接尝试”分离。

**修复要求：** 每次 Connect 创建带 cancel context 的 attempt；事件必须携带 connection ID、attempt ID 和 profile ID；只有当前 generation 可以提交 Connected；Disconnect、Switch、Delete 和 Shutdown 必须取消并 join 当前 attempt。backend 不能在阻塞网络操作期间持有全局互斥锁。

### FC-005 profile 切换语义与实际 VPN 不一致

**类型：已确认错误；状态机。**

`internal/appd/daemon.go:552` 的 `SwitchProfile` 只更新 current profile 并持久化，不断开或重连。CLI 帮助却宣称切换会重连。活动 VPN 可能仍属于 A，而 status/current profile 已显示 B，后续事件还可能被归到 B。

**修复要求：** 明确选择一种产品语义并在 API 类型中表达：

- 若 Switch 表示切换活动连接，则执行有所有权的 replace transaction；
- 若仅改变默认 profile，则重命名为 select，并明确活动 connection 的 profile ID。

不能继续让 `current_profile` 同时承担默认项和活动连接身份。

### FC-006 daemon 正常退出不关闭 VPN、路由、DNS 和后台循环

**类型：已确认错误；主机网络安全。**

`cmd/flexconnectd/main.go` 收到退出信号后仅关闭 HTTP server/listener。`appd.Service` 没有 `Close`，不会停止当前 VPN、SOCKS5、流量 ticker、backend event loop 或 watch；Linux 的直接 `resolv.conf` 路径尤其可能在 SIGTERM 后遗留 VPN DNS。

Tailscale 把 engine、router、DNS 和 watch 的关闭纳入后端生命周期。FlexConnect 的 daemon 退出也必须拥有完整 teardown。

**修复要求：** 增加幂等、带超时和错误聚合的 Service.Close；关闭顺序至少覆盖：拒绝新操作、取消 reconnect/repair/connect attempt、关闭 proxy、断开 backend、恢复 route/DNS、停止后台循环、关闭 watch，最后停止 API。清理错误必须进入退出状态和日志。

### FC-007 动态 split route 路径可并发崩溃并永久污染路由

**类型：已确认错误；并发/输入处理/路由。**

- `internal/anyconnect/tunnel/tun.go` 为每个 DNS 响应启动 goroutine；平台 manager 的 route map 没有 mutex。
- `tun.go:295` 无条件读取 `dns.Questions[0]`，畸形或无 question 的 DNS 包可触发 panic。
- `tun.go:313`、`:331` 忽略 `SetDynamicRoutes` 错误。
- 动态地址没有基于 DNS TTL 的过期和撤销，会一直保留到 tunnel 关闭。
- `internal/anyconnect/utils/utils.go:29` 使用裸 `HasSuffix`，`evilcorp.com` 会匹配 `corp.com`，缺少 DNS label 边界和规范化。

**修复要求：** 由一个串行 route reconciler 接收解析结果；严格校验 DNS message/question；规范化 FQDN 并按 label 匹配；维护域名、IP、TTL 和引用计数；用期望状态 diff 原子更新；失败可观测并重试，关闭时 join reconciler。

## 5. P1：生产阻断问题

### FC-008 AnyConnect backend 存在共享状态数据竞争

**类型：设计缺口，源码存在不一致锁约束。**

`Backend.active` 在 Connect/Disconnect 中由 mutex 保护，但 `activeSession`、SessionInfo、Traffic、ReadServerConfig、RuntimeDiagnostics 和 TunnelDialer 等读路径未使用同一锁。内部 session 字段也会被网络 goroutine 更新。当前 race 测试未覆盖真实 backend 并发。

**修复要求：** 定义唯一所有者或不可变 session snapshot；短锁只用于发布/获取 handle，网络操作不得持锁；session 自身字段使用串行 loop、atomic 或明确 mutex。新增 Connect/Disconnect/Diagnostics/Traffic 并发竞态测试。

### FC-009 profile、secret 与状态文件更新不是事务

**类型：已确认错误；数据一致性。**

- Create：先写 secret、再改内存、最后 persist；persist 失败留下孤儿 secret 和已变内存。
- Update：先改 secret/内存再 persist；失败后返回错误但运行态已改变。
- Delete：先删 secret、再删内存、最后 persist；persist 失败时凭据已经丢失。

Tailscale 的持久化使用 `atomicfile`，并把 prefs/profile 更新置于明确的状态所有权下。FlexConnect 的文件 rename 只解决部分写入，不解决多资源提交。

**修复要求：** 引入 prepare/commit/rollback 顺序；先验证和构造新状态，原子提交 profile 文件，再提交 secret 引用切换，最后删除旧 secret。若底层无法跨存储原子提交，应记录可恢复 journal，并在启动时完成或回滚。

### FC-010 profile 输入缺少不变量验证

**类型：已确认设计缺口。**

Create 接受调用方提供的 `id` 和 `secret_ref`，未强制 ID 唯一、未验证 URL scheme/host、route CIDR、DNS、MTU、SOCKS listen address 等。重复 ID、共享 secret ref、不可达配置和 API 路径冲突都能进入持久状态。

`client/local` 还把 ID 直接拼入 URL path，未做 `PathEscape`；特殊 ID 可与 `current`、`switch` 等路由发生歧义。

**修复要求：** daemon 独占 ID 和 secret ref 分配；建立一处 canonical validator，CLI/API/import/env 都调用；客户端 path segment 必须转义，服务端使用路由参数而不是字符串后缀解析。

### FC-011 存储的 server URL 与实际连接语义不同

**类型：已确认错误。**

profile 规范化允许 `http`，而 AnyConnect backend 实际强制 HTTPS/group access。用户看到和导出的配置不等于运行时使用的配置。

**修复要求：** 只接受明确支持的 scheme；规范化后的 endpoint 应成为唯一事实源，backend 不得静默重写。

### FC-012 MTU 范围与 tunnel buffer 不兼容

**类型：已确认错误。**

配置允许最高 65535 的 MTU，但 tunnel buffer 固定为 2048。大于 buffer 的有效 TUN packet 会导致读取失败或截断，并最终表现为不稳定断线。

**修复要求：** 从协议和平台上限推导合法 MTU，并让 buffer 至少覆盖 MTU 加必要头部；启动时拒绝不一致配置。增加边界 MTU 的包往返测试。

### FC-013 默认 secret store 会静默降级为明文文件

**类型：已确认设计错误；安全。**

`cmd/flexconnectd/env.go` 在默认 keyring 不可用时自动切到 `0600` 的明文 JSON 文件。虽然文档提到这一行为，但运行时没有显式授权，安全姿态会因机器环境悄然变化。

**修复要求：** `keyring` 失败必须 fail fast；需要文件存储时由管理员显式选择并在 status/health 中持续标记 degraded。不要保留自动回退。

### FC-014 Docker 默认暴露无认证 SOCKS5

**类型：已确认安全设计错误。**

Compose 默认将 `0.0.0.0:1080` 发布到宿主机，SOCKS5 server 只支持无认证。默认部署可能成为局域网可访问的开放代理。

**修复要求：** 默认仅绑定 loopback，或要求显式认证/访问策略后才能绑定非 loopback；Compose 不应默认 publish。daemon 应拒绝“非 loopback + 无认证”的组合。

### FC-015 SOCKS5 协议协商和关闭实现错误

**类型：已确认错误。**

- server 无论客户端是否提供 method `0x00` 都回复 no-auth，违反 SOCKS5 method negotiation。
- `Close` 只关 listener 后等待 WaitGroup，不关闭已接受连接；活跃 `io.Copy` 可使 disconnect/update/shutdown 永久阻塞。
- accept/连接处理错误大多被忽略，缺少可观测性。

**修复要求：** 正确选择共同 method，否则回复 `0xff`；追踪 active conns，在 Close 时取消并关闭；所有 goroutine 有 context；对协议拒绝和异常关闭做结构化计数/日志。

### FC-016 VPN 成功后 SOCKS5 启动失败被吞掉

**类型：已确认错误。**

appd 在连接成功后应用 proxy，失败仅记录日志，Connect 仍返回成功。用户要求的 profile 运行态与实际运行态不同，health 仍显示 ok。

**修复要求：** 把 profile 运行配置作为一个提交事务。若 SOCKS5 是 enabled，则启动失败必须使连接进入 degraded/failed，并按明确策略回滚 VPN 或返回部分失败；不能静默成功。

### FC-017 macOS DNS 配置对象错误且无法恢复原配置

**类型：已确认错误；平台实现。**

`internal/osnet/manager_darwin.go:64` 把 utun 名称传给 `networksetup -setdnsservers`，但该命令需要 network service。错误被忽略；Close 又用 `empty`，没有保存和恢复原始 DNS。路由删除错误同样被忽略。

**修复要求：** 使用维护良好的 SystemConfiguration/DNS API 或经过验证的系统集成；准确识别 service；保存被本连接拥有的原配置并有条件恢复；任何设置失败都中止连接事务。补充真实 macOS acceptance，不以交叉编译代替。

### FC-018 Linux DNS 管理会覆盖系统状态

**类型：已确认设计缺口；平台实现。**

- `resolvectl` 路径只设置 interface DNS，未设置 route domains/default-route，查询未必会经过 VPN DNS。
- fallback 整体覆盖 `/etc/resolv.conf`，丢失 search/options，并可能破坏 symlink/network manager 所有权。
- 关闭时无条件写回启动时快照，会覆盖 VPN 期间其他组件的合法更改。

Tailscale `net/dns/manager.go` 区分多种系统 DNS manager，保留 last good config，并检测外部 trample。

**修复要求：** 建立明确的 Linux DNS backend 选择；优先 D-Bus/systemd-resolved 或 NetworkManager API；直接文件模式必须显式启用，保留完整语义，并用 compare-and-restore 避免覆盖他人更改。

### FC-019 路由没有可靠的所有权与回滚

**类型：已确认设计缺口。**

Linux 使用 `RouteReplace`，关闭时按目标删除。若主机原先已有相同路由，FlexConnect 会接管并在退出时删除它。动态 route 更新和 Close 也缺少串行化；部分成功后没有统一 rollback journal。

Tailscale router config 区分 Routes、LocalRoutes 和 SubnetRoutes，并以整份期望配置驱动 reconciliation。FlexConnect 应采用同类“期望状态 + 拥有记录”模型。

**修复要求：** 应用前读取并记录冲突路由；只删除本 attempt 创建且仍匹配的对象；每次 route/DNS 变更生成 transaction record；部分失败逆序回滚。

### FC-020 Windows 本地控制面授权过宽

**类型：已确认安全设计缺口。**

named pipe SDDL 给 Builtin Users 通用读写权限。API 内没有连接身份、operator 或读写权限判断，因此普通本地用户可以读取 profile/diagnostics，并发起连接、删除、导入和 secret 相关操作。

Tailscale 的 `ipn/ipnserver/server.go` 从连接身份派生 operator 与 `PermitRead`/`PermitWrite`，LocalAPI handler 再逐路由检查。

**修复要求：** 明确 Windows operator/admin 模型；从 pipe client token 获取身份；读和写分权；敏感操作要求管理员或被授权 operator；ACL 仅作为第一层，不替代应用层授权。

## 6. P2：可靠性与工程缺口

### FC-021 watch 对慢消费者静默丢事件

每个 watcher 缓冲区满时，`emitLocked` 直接丢弃事件，只写 debug log；没有单调 sequence、gap 标志或强制重同步。客户端会继续保持连接，却永久持有过时状态。

Tailscale watch 支持 initial snapshot、mask、受控速率和明确的慢 watcher 行为。FlexConnect 至少应给事件增加 sequence/revision；发生丢弃时关闭流或发送 gap，客户端重新获取 Status + Diagnostics 后恢复。

### FC-022 状态快照存在浅拷贝和可变别名

Status、profile 列表和 watch payload 的若干 slice/pointer 只做浅拷贝。后台更新可能改变已发布快照的底层数据，也增加 race 风险。Tailscale 大量使用 clone/immutable view 维持跨 goroutine 边界。

**修复要求：** 定义 `Clone`/snapshot 边界；进入事件队列或离开锁前深拷贝全部可变字段；测试消费者保存旧快照时其内容不再变化。

### FC-023 流量速率可发生 uint64 下溢

流量采样直接用当前累计值减上次累计值。如果 backend 重置计数但服务仍为 Connected，差值会下溢成巨大正数，产生荒谬速率。

**修复要求：** 检测 counter reset/connection generation 变化；每个 connection ID 独立维护基线；重置样本不计算速率。

### FC-024 `/v1/health` 不是真实健康状态

health handler 只要 HTTP 可响应就固定返回 `status: ok`。它不能反映 state persist、secret store、DNS、route、underlay monitor、SOCKS5、backend 或 reconnect exhausted 的故障。

Tailscale 有集中 health tracker。FlexConnect 应建立组件化 health registry，区分 ready、degraded、failed，并让诊断、托盘和容器 healthcheck 使用同一事实源。

### FC-025 appd 成为过度集中的可变状态对象

`internal/appd/daemon.go` 同时拥有 profile、持久化、连接状态、重连、network repair、代理、诊断、流量、事件和 update。这不是单纯文件过长问题，而是多个 goroutine 可以通过不同入口改变同一生命周期，造成 FC-003 至 FC-009。

**修复要求：** 不需要复制 Tailscale 的规模，但应把状态改变收敛到一个 command loop；profile repository、connection supervisor、network transaction、proxy supervisor、health/event bus 各自有清晰所有者。API handler 只提交命令，不直接组合跨组件事务。

### FC-026 updater 自制版本比较语义错误

`internal/updater/checker.go` 把非数字 segment 当作 0，并忽略 prerelease 语义。非法 tag 和 `1.2.3-beta` 可能被错误排序。仓库已有可用的 semver 能力，不应继续维护不完整解析器。

**修复要求：** 使用成熟 semver 实现；严格拒绝非法版本；明确是否允许 prerelease channel；为 build metadata、prerelease 和 `v` 前缀补测试。

### FC-027 认证响应无大小上限

`internal/anyconnect/auth/auth.go:282` 对响应体执行无界 `io.ReadAll`。错误或恶意服务器可以消耗任意内存；结合 FC-001，攻击条件更低。

**修复要求：** 对每类协议响应设置合理上限，超限返回带阶段信息的错误；统一 HTTP client 的 timeout、redirect、TLS 和 body-limit 策略。

### FC-028 状态文件写入不满足崩溃一致性

`internal/store/file` 使用固定 `.tmp` 和 rename，但没有对文件和目录 fsync，也没有安全创建临时文件。断电时 rename 不等于持久提交；在自定义、权限不严的目录还存在固定临时路径风险。

Tailscale `atomicfile/atomicfile.go` 提供专门的原子替换抽象。FlexConnect 应统一 profile/secret 文件持久化策略：同目录随机临时文件、限制权限、flush/fsync、rename、目录 fsync，并对 Windows replacement 语义测试。

## 7. P3：协议与文档一致性

### FC-029 本地 API 的协议细节不稳健

- daemon/client 要求 Local API version 完全相等，没有能力协商，滚动升级容易出现不必要的不兼容。
- API 错误大多映射为 HTTP 400，无法区分 404、409、超时、后端不可用和内部错误；客户端也缺少稳定机器码。
- Create handler 在设置 JSON Content-Type 前先写 201，响应可能缺少正确 Content-Type。
- 没有 Host/Origin/Referer 防护。当前本地 transport 降低了浏览器直接利用概率，但一旦增加 web bridge 或错误暴露 socket，边界会变化。Tailscale LocalAPI 显式校验这些字段并发送 capability header。

**修复要求：** 建立稳定 error envelope/code；合理使用 HTTP 状态；握手改为 protocol version + capabilities；统一安全 header 和请求来源策略。

### FC-030 文档已不是单一事实源

已确认不一致包括：

- AGENTS 描述 browser console 和 `flexconnect web`，实际没有对应命令/实现；
- 引用不存在的 `docs/completion-audit.md`、smoke/live 脚本；
- 声称没有 CI，但仓库存在 release workflows；
- 声称 `go vet` 当前失败，本次实际通过；
- 声称无根 LICENSE/包元数据 Proprietary，实际存在 MIT LICENSE 且包元数据为 MIT；
- README 写自动重连最多 10 次，代码常量为 3；
- CLI switch 帮助宣称会重连，但实现只改 current profile。

**修复要求：** 将运行时默认值和 CLI help 从共享常量/类型生成；删除不存在功能的描述；CI 校验文档中的命令和路径；架构变更必须与代码同提交更新文档。

## 8. 额外实现观察

### 8.1 server route 解析丢失 exclude

AnyConnect session 对 include/exclude 属性采用 `if ... else if ...`。若服务器同时提供两者，exclude 会被丢弃。应分别解析并交给 route planner，补同时存在的协议 fixture。

### 8.2 本地接口发现依赖公网探测

Linux/macOS 使用到公共地址的 route heuristic 推断本地接口。离线、captive portal、多策略路由和只允许企业出口时可能选错或失败。Tailscale 使用 OS route table/netmon 作为持续事实源。FlexConnect 应将 Windows 已有的 underlay 选择思路扩展为跨平台 network monitor，而不是在连接时临时探测单一公网地址。

### 8.3 Connected 事件发布早于 underlay monitor 成功

backend 先发布 connected，再启动 underlay monitor；monitor 启动失败后 Connect 返回错误，但 appd 已经观察过成功事件。这是 FC-004 的具体实例，应让 attempt 只在全部必需组件准备完成后一次性提交成功。

## 9. 验证结果与证据边界

本次在未读取任何 live credential、未运行真实 AnyConnect、未安装服务的前提下执行：

| 验证 | 结果 | 说明 |
| --- | --- | --- |
| `go test ./...` | 通过 | 全部 Go package 测试通过 |
| 关键包 `go test -race` | 通过 | appd、tunnel、osnet、socks5、apiserver、local client；未覆盖真实 backend 竞争路径 |
| `go vet ./...` | 通过 | 当前基线未复现文档所述 vet 失败 |
| 本机 `go build ./...` | 通过 | Windows 构建通过 |
| Linux amd64、CGO disabled 交叉构建 | 通过 | 不等于 Linux 路由/DNS/TUN 运行验收 |
| Darwin arm64、CGO disabled 交叉构建 | 失败 | `fyne.io/systray` 依赖原生 CGO；该结果不能单独判定 macOS 产品失败 |
| `govulncheck` | 未执行 | 本机未安装，不能声称依赖无漏洞 |

现有测试通过不能否定本报告问题。关键缺口恰好位于当前测试未覆盖的边界：真实 TLS 身份、连接取消/迟到事件、活动 profile 删除、SIGTERM 清理、动态 DNS 并发、既有 route 所有权、真实 macOS/Linux DNS、Windows 多用户授权和长连接 proxy 关闭。

## 10. 建议修复顺序

### 阶段 A：立即封堵安全与不可恢复状态

1. 修复 FC-001、FC-002，统一 TLS trust 和随机数错误传播；
2. 设计 attempt/generation 模型，修复 FC-003 至 FC-006；
3. 串行化动态 route，修复 FC-007；
4. 收紧 Windows 控制面和 Docker SOCKS5 默认值，修复 FC-014、FC-020。

验收门槛：连接可以在任意阶段取消；迟到事件不能复活；daemon 退出恢复网络；未受信服务器无法得到凭据；普通未授权用户不能修改 daemon。

### 阶段 B：建立网络配置事务

1. 把 route、DNS、proxy 纳入同一 connection transaction；
2. 实现 Linux/macOS 的拥有、回滚和 compare-and-restore；
3. 建立串行 reconciler、TTL 动态路由和 underlay monitor；
4. 用真实平台测试验证从零连接、断线、重连、进程退出和异常恢复。

验收门槛：任何中间步骤失败均不残留 TUN、route、DNS、proxy；不会覆盖其他网络管理器在 VPN 期间做出的更改。

### 阶段 C：收敛状态和可观测性

1. 以 connection supervisor command loop 作为唯一状态写入者；
2. profile/secret 持久化改为可恢复事务；
3. watch 增加 revision/gap/resync，所有 payload 使用不可变 snapshot；
4. 建立真实 health registry 和结构化错误码。

验收门槛：API、CLI、tray 和 diagnostics 对同一 connection/profile 身份达成一致；慢消费者可检测并恢复；健康状态能解释具体退化组件。

### 阶段 D：平台与发布完成度

1. 修复 macOS/Linux 网络 backend 并做真实平台 acceptance；
2. 增加 PR CI：unit、race、vet、smoke、govulncheck、跨平台 build/package；
3. release 在打包前执行 gates，并增加 checksum、SBOM、签名/公证和 provenance；
4. 修复全部文档漂移，使 README/AGENTS/help 与代码同源。

## 11. 不应照搬 Tailscale 的部分

FlexConnect 不需要复制 Tailscale 的控制服务器、节点密钥、DERP、netmap、ACL 分发或 mesh engine。建议复用的是成熟约束，而非代码规模：

- 每个连接尝试有唯一身份和单一所有者；
- 网络设置是可回滚的期望状态事务；
- 本地 API 基于真实连接身份授权；
- 健康状态来自组件事实，而不是进程存活；
- watch 能检测 gap 并重建 snapshot；
- 退出是完整生命周期的一部分。

在这些基础约束完成前继续增加浏览器控制台、更多 profile 功能或发布格式，会扩大状态空间并使现有根因更难修复。

## 12. FlexConnect 1.3.0 修复矩阵

本节按最终批准的 1.3.0 取舍覆盖上文的早期建议：不支持自签 CA/pin，不新增
SOCKS5 认证，不执行 `govulncheck`，不生成 checksum/SBOM/签名/公证/provenance，且只把
自动测试作为发布证据。

| Finding | 1.3.0 处理 | 自动化证据边界 |
| --- | --- | --- |
| FC-001 | 认证、CSTP、DTLS、netcheck 只用系统 CA 和严格主机名校验；删除跳过验证配置 | 自签服务器在发送凭据前失败；未执行真实企业 CA 验收 |
| FC-002 | profile、epoch、attempt、connection、operation 和 master secret 的随机错误全部上抛 | 注入失败测试覆盖 ID、attempt 和 secret 生成 |
| FC-003 | 活动 profile 删除先取消并完成连接清理，再执行持久化删除事务 | fake backend 删除/清理失败路径测试 |
| FC-004 | attempt/connection/generation 身份、取消 context 和迟到结果拒绝 | Connecting 取消与迟到成功测试、race gate |
| FC-005 | `PUT /v2/connection` 原子切换；selected 与 connected 身份分离；失败保留新 selected | supervisor/profile 集成测试 |
| FC-006 | daemon 以 30 秒总预算有序关闭 proxy、backend、watch 和循环；清理失败 fatal | cleanup failure 与 Close 测试 |
| FC-007 | 动态 DNS route 由单 worker 串行 reconcile，含严格 DNS 校验、TTL、引用和 4096 上限 | 畸形 DNS、label、TTL、共享 IP、上限与 race 测试 |
| FC-008 | backend 阻塞 Connect 不持发布 mutex，对外 session/runtime/traffic 深拷贝 | backend/appd race gate |
| FC-009 | profile/secret 使用持久 intent：intent、new secret、state、old secret、clear | 启动恢复和 secret/store 失败测试 |
| FC-010 | URL、名称、scope/owner、route、DNS、listen、MTU 和列表统一 validator | API 422、strict startup 与 validator 测试 |
| FC-011 | server 仅允许 HTTPS host、可选 port/path；拒绝 userinfo、query、fragment、opaque 和 IPv6 literal | profile/API validation 测试 |
| FC-012 | MTU 固定 576–9000，packet buffer 按 MTU 与协议头分配 | tunnel buffer/MTU 测试 |
| FC-013 | 默认 keyring probe 失败即启动失败；`file` 仅显式配置并强制权限/ACL | secret store 与权限测试 |
| FC-014 | 默认 SOCKS5 loopback；Compose 宿主只映射 `127.0.0.1`；显式 non-loopback 视为管理员授权 | profile validation 与 Compose 文档检查 |
| FC-015 | 严格 no-auth method 协商，跟踪 accepted connection，带超时关闭并记录错误 | 无共同 method 与活动连接 Close 测试 |
| FC-016 | 启用的 SOCKS5 是连接必需组件；启动/运行失败回滚 VPN | appd proxy rollback 测试 |
| FC-017 | macOS DNS 使用固定 SystemConfiguration key 和结构化 `scutil` stdin；route 不经 shell | scutil contract 与跨编译；未执行真实 macOS 网络验收 |
| FC-018 | Linux DNS 只用 resolved、NetworkManager 或 resolvconf，无法识别 owner 时失败；不写 resolv.conf | backend selection mock；未执行真实桌面 DNS 验收 |
| FC-019 | 持久 network ownership journal、精确 add/delete、compare-before-delete 和启动恢复 | journal 单测与 CI network namespace 事务 |
| FC-020 | Windows named pipe token 派生 SID/SYSTEM/elevation；user owner 隔离与 machine lock | Windows named-pipe identity 和 actor 矩阵测试 |
| FC-021 | daemon epoch、单调 revision、1024 ring replay、gap snapshot 和慢消费者断开 | replay、ring gap、resync 测试 |
| FC-022 | status、profile、runtime、diagnostics、watch 的 slice/map/pointer 深拷贝 | appd/apiserver race 与隔离测试 |
| FC-023 | 流量 baseline 随 connection generation 重置，计数回退按 reset 处理 | traffic reset/downshift 测试 |
| FC-024 | `/v2/live` 与组件化 `/v2/ready` 分离，machine/cleanup/dynamic-route 故障影响 ready | live/capabilities/ready API 测试 |
| FC-025 | API operation、network repair、自动重连和 proxy failure 进入同一 supervisor 串行边界；repository 事务独立 | supervisor competition 与 race 测试 |
| FC-026 | 版本比较改用直接依赖 `Masterminds/semver/v3` | updater 版本测试 |
| FC-027 | 认证/XML 响应硬限制 4 MiB，并报告阶段和限制 | auth response limit 单测 |
| FC-028 | 同目录随机临时文件、0600/ACL、文件 fsync、原子替换和目录 fsync/write-through | store/secret 原子性、损坏 JSON 和权限测试 |
| FC-029 | Local API v2-only、结构化错误、request ID、Host/Origin/Referer/body/path 校验及先 header 后 status | apiserver v2 协议测试 |
| FC-030 | 删除 browser/web 与不存在脚本声明；README、AGENTS、CLI help、ARCH、CI、版本和许可证同步 | 全仓测试、help 测试和引用扫描 |

本地通过的门禁为 `go test ./...`、指定并发敏感包的 `go test -race`、`go vet ./...`、
`go build ./...`、Windows ZIP/MSI 打包及 Linux/macOS 核心包跨编译。Docker 本地构建两次均在
拉取 Docker Hub frontend 时发生 TLS handshake timeout，未进入仓库构建步骤，因此 Docker
成功证据必须以后续 GitHub Actions 结果为准。
