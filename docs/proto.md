# Protocol Documentation
<a name="top"></a>

## Table of Contents

- [litesentry.proto](#litesentry-proto)
    - [ContainerMem](#litesentry-ContainerMem)
    - [ContainerMetrics](#litesentry-ContainerMetrics)
    - [Disk](#litesentry-Disk)
    - [HostMetrics](#litesentry-HostMetrics)
    - [IPAddr](#litesentry-IPAddr)
    - [Mem](#litesentry-Mem)
    - [MetricsBatch](#litesentry-MetricsBatch)
    - [Net](#litesentry-Net)
    - [PushAck](#litesentry-PushAck)
    - [Series](#litesentry-Series)
    - [Series.FieldsEntry](#litesentry-Series-FieldsEntry)
    - [Series.TagsEntry](#litesentry-Series-TagsEntry)
  
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






<a name="litesentry-Disk"></a>

### Disk



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| mount | [string](#string) |  |  |
| fs | [string](#string) |  |  |
| total | [uint64](#uint64) |  |  |
| used | [uint64](#uint64) |  |  |






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
| series | [Series](#litesentry-Series) | repeated | 阶段二启用，阶段一不填充 |






<a name="litesentry-Net"></a>

### Net
网络速率（前后两采样差分得到）


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| rx_bps | [uint64](#uint64) |  |  |
| tx_bps | [uint64](#uint64) |  |  |






<a name="litesentry-PushAck"></a>

### PushAck



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| server_time | [string](#string) |  |  |
| message | [string](#string) |  |  |






<a name="litesentry-Series"></a>

### Series



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  | e.g. &#34;postgres.connections&#34; |
| tags | [Series.TagsEntry](#litesentry-Series-TagsEntry) | repeated | 维度: instance, db... |
| fields | [Series.FieldsEntry](#litesentry-Series-FieldsEntry) | repeated | 指标: active=12 |
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





 

 

 


<a name="litesentry-Agent"></a>

### Agent


| Method Name | Request Type | Response Type | Description |
| ----------- | ------------ | ------------- | ------------|
| Push | [MetricsBatch](#litesentry-MetricsBatch) | [PushAck](#litesentry-PushAck) | 常规定时上报（10s/批） |
| Stream | [MetricsBatch](#litesentry-MetricsBatch) stream | [PushAck](#litesentry-PushAck) stream | 可选：长连接流式，实时性要求高时启用（阶段二插件分发复用此通道） |

 



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

