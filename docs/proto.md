# Protocol Documentation
<a name="top"></a>

## Table of Contents

- [litesentry.proto](#litesentry-proto)
    - [Chunk](#litesentry-Chunk)
    - [ContainerMem](#litesentry-ContainerMem)
    - [ContainerMetrics](#litesentry-ContainerMetrics)
    - [DesiredState](#litesentry-DesiredState)
    - [Disk](#litesentry-Disk)
    - [FrpStatus](#litesentry-FrpStatus)
    - [FrpTunnelStatus](#litesentry-FrpTunnelStatus)
    - [HostMetrics](#litesentry-HostMetrics)
    - [IPAddr](#litesentry-IPAddr)
    - [Mem](#litesentry-Mem)
    - [MetricsBatch](#litesentry-MetricsBatch)
    - [Net](#litesentry-Net)
    - [PluginRequest](#litesentry-PluginRequest)
    - [PluginSpec](#litesentry-PluginSpec)
    - [PushAck](#litesentry-PushAck)
    - [RegisterReply](#litesentry-RegisterReply)
    - [RegisterRequest](#litesentry-RegisterRequest)
    - [Series](#litesentry-Series)
    - [Series.FieldsEntry](#litesentry-Series-FieldsEntry)
    - [Series.TagsEntry](#litesentry-Series-TagsEntry)
    - [TaskRunReport](#litesentry-TaskRunReport)
    - [TaskSpec](#litesentry-TaskSpec)
  
    - [Agent](#litesentry-Agent)
  
- [Scalar Value Types](#scalar-value-types)



<a name="litesentry-proto"></a>
<p align="right"><a href="#top">Top</a></p>

## litesentry.proto
litesentry 数据契约（唯一 proto 源）

阶段一：Agent 定时上报主机 &#43; 容器指标。
- 通信走 IPv4，Agent 通过 gRPC metadata 携带 token（证书 TLS/mTLS 预留，
  测试阶段明文联调，上线前启用）。
- Series 通用字段已预留，阶段二插件化直接启用。


<a name="litesentry-Chunk"></a>

### Chunk



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| data | [bytes](#bytes) |  | 二进制分块 |
| sha256 | [string](#string) |  | 完整二进制 SHA-256（末尾分块携带，用于校验） |
| size | [uint64](#uint64) |  | 完整二进制字节数（末尾分块携带） |
| error | [string](#string) |  | 错误时返回 |






<a name="litesentry-ContainerMem"></a>

### ContainerMem
容器内存（usage/limit，区别于主机的 total/used）


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| usage | [uint64](#uint64) |  |  |
| limit | [uint64](#uint64) |  |  |






<a name="litesentry-ContainerMetrics"></a>

### ContainerMetrics



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| name | [string](#string) |  |  |
| image | [string](#string) |  |  |
| state | [string](#string) |  |  |
| restarts | [uint32](#uint32) |  |  |
| uptime_s | [uint64](#uint64) |  |  |
| cpu_pct | [float](#float) |  |  |
| mem | [ContainerMem](#litesentry-ContainerMem) |  |  |
| net | [Net](#litesentry-Net) |  |  |






<a name="litesentry-DesiredState"></a>

### DesiredState
心跳响应下发的期望状态：Agent 发现 state_version != 已应用版本时重新应用。


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| state_version | [uint64](#uint64) |  | 全局递增，任一配置变更联动 &#43;1 |
| frp_toml | [string](#string) |  | 本机要运行的 frp 配置（frps 或 frpc），空串 = 不管理 |
| frp_enabled | [bool](#bool) |  | 是否启用 frp 进程管理 |
| plugins | [PluginSpec](#litesentry-PluginSpec) | repeated | manifest 期望版本 |
| tasks | [TaskSpec](#litesentry-TaskSpec) | repeated | 本机定时任务定义 |






<a name="litesentry-Disk"></a>

### Disk



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| mount | [string](#string) |  |  |
| fs | [string](#string) |  |  |
| total | [uint64](#uint64) |  |  |
| used | [uint64](#uint64) |  |  |






<a name="litesentry-FrpStatus"></a>

### FrpStatus



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| running | [bool](#bool) |  | frp 进程是否存活 |
| frp_version | [string](#string) |  |  |
| error | [string](#string) |  | 未安装 / 启动失败等原因 |
| tunnels | [FrpTunnelStatus](#litesentry-FrpTunnelStatus) | repeated |  |






<a name="litesentry-FrpTunnelStatus"></a>

### FrpTunnelStatus



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| type | [string](#string) |  | tcp | udp | http | https | stcp ... |
| status | [string](#string) |  | online | offline | error |
| err | [string](#string) |  |  |
| rx_bytes | [uint64](#uint64) |  |  |
| tx_bytes | [uint64](#uint64) |  |  |






<a name="litesentry-HostMetrics"></a>

### HostMetrics



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| hostname | [string](#string) |  |  |
| os | [string](#string) |  |  |
| arch | [string](#string) |  |  |
| kernel | [string](#string) |  |  |
| uptime_s | [uint64](#uint64) |  |  |
| load_1m | [float](#float) |  |  |
| load_5m | [float](#float) |  |  |
| cpu_pct | [float](#float) |  |  |
| mem | [Mem](#litesentry-Mem) |  |  |
| swap | [Mem](#litesentry-Mem) |  |  |
| disks | [Disk](#litesentry-Disk) | repeated |  |
| net | [Net](#litesentry-Net) |  |  |
| ips | [IPAddr](#litesentry-IPAddr) | repeated | IPv4 &#43; IPv6 全量地址 |
| agent_cpu_pct | [float](#float) |  | Agent 自身进程占用（读 /proc/self；用于监控采集端自身开销）

自身进程周期 CPU 占用（%） |
| agent_mem_rss | [uint64](#uint64) |  | 自身 RSS（字节） |






<a name="litesentry-IPAddr"></a>

### IPAddr
网卡地址（IPv4 / IPv6，含 iface 与 scope）


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| family | [string](#string) |  | ipv4 | ipv6 |
| addr | [string](#string) |  |  |
| iface | [string](#string) |  |  |
| scope | [string](#string) |  | global | link | loopback |






<a name="litesentry-Mem"></a>

### Mem



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| total | [uint64](#uint64) |  |  |
| used | [uint64](#uint64) |  |  |






<a name="litesentry-MetricsBatch"></a>

### MetricsBatch



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| agent_id | [string](#string) |  |  |
| ts | [uint64](#uint64) |  | unix 秒 |
| host | [HostMetrics](#litesentry-HostMetrics) |  |  |
| containers | [ContainerMetrics](#litesentry-ContainerMetrics) | repeated |  |
| series | [Series](#litesentry-Series) | repeated | 插件统一输出（阶段二启用） |
| frp | [FrpStatus](#litesentry-FrpStatus) |  | 本机 frp 隧道状态（阶段二） |
| task_runs | [TaskRunReport](#litesentry-TaskRunReport) | repeated | 定时任务执行结果（阶段二） |






<a name="litesentry-Net"></a>

### Net
网络速率（前后两采样差分得到）


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| rx_bps | [uint64](#uint64) |  |  |
| tx_bps | [uint64](#uint64) |  |  |






<a name="litesentry-PluginRequest"></a>

### PluginRequest
拉插件二进制请求 / 流式分块。


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| plugin_id | [string](#string) |  |  |
| version | [string](#string) |  |  |






<a name="litesentry-PluginSpec"></a>

### PluginSpec
插件期望清单（manifest）：Server 指派本机应运行的插件与版本。


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| plugin_id | [string](#string) |  |  |
| version | [string](#string) |  |  |
| args_json | [string](#string) |  | 插件参数（JSON 字符串） |






<a name="litesentry-PushAck"></a>

### PushAck



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| server_time | [string](#string) |  |  |
| message | [string](#string) |  |  |
| desired_state | [DesiredState](#litesentry-DesiredState) |  | 心跳驱动下发：frp 配置 &#43; 插件 manifest &#43; 任务定义 |






<a name="litesentry-RegisterReply"></a>

### RegisterReply



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| agent_id | [string](#string) |  | Server 分配：已注册过则复用历史 id，否则新建 UUID |
| server_time | [string](#string) |  |  |






<a name="litesentry-RegisterRequest"></a>

### RegisterRequest
节点注册请求：携带机器自身信息，由 Server 分发 / 复用 agent_id。


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| hostname | [string](#string) |  |  |
| machine_id | [string](#string) |  | 机器指纹（Linux /etc/machine-id；缺省回退 hostname） |
| os | [string](#string) |  |  |
| arch | [string](#string) |  |  |
| kernel | [string](#string) |  |  |
| version | [string](#string) |  | Agent 构建版本（CARGO_PKG_VERSION） |






<a name="litesentry-Series"></a>

### Series



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  | e.g. &#34;host.cpu&#34; |
| tags | [Series.TagsEntry](#litesentry-Series-TagsEntry) | repeated | 维度: agent_id, mount, container_id... |
| fields | [Series.FieldsEntry](#litesentry-Series-FieldsEntry) | repeated | 指标: pct=12.3 |
| ts | [uint64](#uint64) |  |  |






<a name="litesentry-Series-FieldsEntry"></a>

### Series.FieldsEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| key | [string](#string) |  |  |
| value | [double](#double) |  |  |






<a name="litesentry-Series-TagsEntry"></a>

### Series.TagsEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| key | [string](#string) |  |  |
| value | [string](#string) |  |  |






<a name="litesentry-TaskRunReport"></a>

### TaskRunReport



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| task_id | [string](#string) |  |  |
| started_at | [uint64](#uint64) |  |  |
| finished_at | [uint64](#uint64) |  |  |
| status | [string](#string) |  | ok | failed | timeout | skipped |
| exit_code | [int32](#int32) |  |  |
| output | [string](#string) |  | stdout/stderr 截断尾部 |






<a name="litesentry-TaskSpec"></a>

### TaskSpec
定时任务定义：由目标 agent 本地 cron 触发执行。


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| task_id | [string](#string) |  |  |
| cron | [string](#string) |  | 标准 5 段 cron |
| plugin_id | [string](#string) |  |  |
| args_json | [string](#string) |  |  |
| timeout_s | [uint32](#uint32) |  |  |
| run_now | [bool](#bool) |  | 立即运行标记：Server 置位，agent 下个心跳立即执行一次，收到报告后 Server 清除 |





 

 

 


<a name="litesentry-Agent"></a>

### Agent


| Method Name | Request Type | Response Type | Description |
| ----------- | ------------ | ------------- | ------------|
| Register | [RegisterRequest](#litesentry-RegisterRequest) | [RegisterReply](#litesentry-RegisterReply) | 节点注册：Agent 启动时用自身机器信息换取（或复用）agent_id |
| Push | [MetricsBatch](#litesentry-MetricsBatch) | [PushAck](#litesentry-PushAck) | 常规定时上报（默认 60s/批，即心跳）；响应携带 DesiredState 下发配置 |
| FetchPlugin | [PluginRequest](#litesentry-PluginRequest) | [Chunk](#litesentry-Chunk) stream | 拉插件二进制（流式分块）：Agent 在 DesiredState 发现缺/旧时主动调用 |
| Stream | [MetricsBatch](#litesentry-MetricsBatch) stream | [PushAck](#litesentry-PushAck) stream | 可选：长连接流式，实时性要求高时启用（预留） |

 



## Scalar Value Types

| .proto Type | Notes | C++ | Java | Python | Go | C# | PHP | Ruby |
| ----------- | ----- | --- | ---- | ------ | -- | -- | --- | ---- |
| <a name="double" /> double |  | double | double | float | float64 | double | float | Float |
| <a name="float" /> float |  | float | float | float | float32 | float | float | Float |
| <a name="int32" /> int32 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint32 instead. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="int64" /> int64 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint64 instead. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="uint32" /> uint32 | Uses variable-length encoding. | uint32 | int | int/long | uint32 | uint | integer | Bignum or Fixnum (as required) |
| <a name="uint64" /> uint64 | Uses variable-length encoding. | uint64 | long | int/long | uint64 | ulong | integer/string | Bignum or Fixnum (as required) |
| <a name="sint32" /> sint32 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int32s. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="sint64" /> sint64 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int64s. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="fixed32" /> fixed32 | Always four bytes. More efficient than uint32 if values are often greater than 2^28. | uint32 | int | int | uint32 | uint | integer | Bignum or Fixnum (as required) |
| <a name="fixed64" /> fixed64 | Always eight bytes. More efficient than uint64 if values are often greater than 2^56. | uint64 | long | int/long | uint64 | ulong | integer/string | Bignum |
| <a name="sfixed32" /> sfixed32 | Always four bytes. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="sfixed64" /> sfixed64 | Always eight bytes. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="bool" /> bool |  | bool | boolean | boolean | bool | bool | boolean | TrueClass/FalseClass |
| <a name="string" /> string | A string must always contain UTF-8 encoded or 7-bit ASCII text. | string | String | str/unicode | string | string | string | String (UTF-8) |
| <a name="bytes" /> bytes | May contain any arbitrary sequence of bytes. | string | ByteString | str | []byte | ByteString | string | String (ASCII-8BIT) |

