* [ ] 

# Drafty：IM 富文本消息格式规范

Drafty 是 IM 用于定义和排版富文本消息的数据格式。Drafty 的设计初衷是在提供丰富排版能力的同时，避免引入过多的安全风险（如 XSS 攻击）。可以将其理解为由 JSON 封装的 [Markdown](https://en.wikipedia.org/wiki/Markdown)。Drafty 受到 Facebook [draft.js](https://draftjs.org/) 规范的启发。[Go 语言实现](../server/drafty/drafty.go) 可以将 Drafty 转换为纯文本和富文本预览。

---

## 示例展示

假设原文本为：

> 这是 **加粗**, `代码块` 以及 _斜体_, ~~删除线~~
> 组合 **加粗与 _斜体_**
> 链接: https://www.example.com/abc#fragment 以及 _[https://www.example.com](https://www.example.com)_
> 包含 [@用户提到](#) 与 [#话题标签](#)

对应的 Drafty-JSON 表达结构：

```json
{
   "txt":  "this is bold, code and italic, strike combined bold and italic an url: https://www.example.com/abc#fragment and another www.example.com this is a @mention and a #hashtag in a string second #hashtag",
   "fmt": [
       { "at":8, "len":4,"tp":"ST" },{ "at":14, "len":4, "tp":"CO" },{ "at":23, "len":6, "tp":"EM"},
       { "at":31, "len":6, "tp":"DL" },{ "tp":"BR", "len":1, "at":37 },{ "at":56, "len":6, "tp":"EM" },
       { "at":47, "len":15, "tp":"ST" },{ "tp":"BR", "len":1, "at":62 },{ "at":120, "len":13, "tp":"EM" },
       { "at":71, "len":36, "key":0 },{ "at":120, "len":13, "key":1 },{ "tp":"BR", "len":1, "at":133 },
       { "at":144, "len":8, "key":2 },{ "at":159, "len":8, "key":3 },{ "tp":"BR", "len":1, "at":179 },
       { "at":187, "len":8, "key":3 },{ "tp":"BR", "len":1, "at":195 }
   ],
   "ent": [
       { "tp":"LN", "data":{ "url":"https://www.example.com/abc#fragment" } },
       { "tp":"LN", "data":{ "url":"http://www.example.com" } },
       { "tp":"MN", "data":{ "val":"mention" } },
       { "tp":"HT", "data":{ "val":"hashtag" } }
   ]
}
```

---

## 核心数据结构

Drafty 对象由三个字段组成：纯文本 `txt`、行内排版样式 `fmt` 和 实体扩展对象 `ent`。

### 1. 纯文本 `txt`

发送的消息首先会被剥离所有格式，转换为纯 Unicode 文本保存在 `txt` 字段中。最简单的有效 Drafty 消息可以仅包含 `txt` 字段。

### 2. 行内排版样式 `fmt`

行内样式是存储在 `fmt` 数组中的样式集合。每个样式包含 `at`（字符偏移起始位置，自 0 开始）和 `len`（排版作用的字符长度）。第三个字段为 `tp`（排版类型）或 `key`（实体索引）：

提供 `tp` 时，表示基础样式修饰：

* `BR`：换行符 (Line Break)。
* `CO`：等宽代码块/行内代码 (Code)。
* `DL`：删除线 (Strikethrough / Deleted)。
* `EM`：斜体强调 (Emphasized / Italic)。
* `FM`：表单 / 表单字段集合。
* `HD`：隐藏内容 (Hidden)。
* `HL`：高亮突出文本 (Highlighted)。
* `RW`：排版逻辑行 (Row)。
* `ST`：粗体/加粗 (Strong / Bold)。

提供 `key` 时，表示指向 `ent` 实体数组中的索引下标（从 0 开始），包含图片、文件、URL 等扩展对象：

* `AU`：嵌入式音频记录。
* `BN`：交互式按钮。
* `EX`：常规文件附件。
* `FM`：表单实体。
* `HT`：话题标签 (#hashtag)。
* `IM`：行内图片。
* `LN`：超链接 (URL)。
* `MN`：用户提及 (@mention)。
* `VC`：音视频通话状态。
* `VD`：行内视频。

**注意**：`at` 和 `len` 的计算单位为 **Unicode Code Point（代码点）**，而非字节数。

---

### 3. 实体扩展对象 `ent`

实体是需要附加扩展数据（可能较大）的元素。由 `tp`（实体类型）和 `data`（与类型相关的详细元数据）组成。

#### `IM`：行内或嵌入式图片

```json
{
  "tp": "IM",
  "data": {
    "mime": "image/png",
    "val": "Rt53jUU...iVBORw0KGgoA==", // Base64 编码的带内小缩略图/图片数据
    "ref": "/v0/file/s/abcdef12345.jpg", // 带外大图下载 URL
    "width": 512,
    "height": 512,
    "name": "sample_image.png",
    "size": 123456
  }
}
```

#### `AU`：语音与音频录音

```json
{
  "tp": "AU",
  "data": {
    "mime": "audio/aac",
    "ref": "/v0/file/s/e769gvt1ILE.m4v",
    "preview": "Aw4JKBkAAAAKMSM...vHxgcJhsgESAY", // 音频振幅预览条 Base64
    "duration": 180000, // 播放时长（毫秒）
    "size": 595496
  }
}
```

#### `BN`：交互式卡片按钮

```json
{
  "tp": "BN",
  "data": {
    "name": "confirmation",
    "act": "url", // 动作类型：pub (发送数据消息), url (发起 HTTP GET 请求)
    "val": "some-value",
    "ref": "https://www.example.com/path/?foo=bar"
  }
}
```

#### `EX`：通用文件附件

```json
{
  "tp": "EX",
  "data": {
    "mime": "application/pdf",
    "ref": "/v0/file/s/abcdef12345.pdf",
    "name": "document.pdf",
    "size": 1234567
  }
}
```

#### `VC`：音视频通话控制状态

```json
{
  "tp": "VC",
  "data": {
    "duration": 10000, // 通话时长（毫秒）
    "state": "finished", // 状态：accepted, busy, finished, disconnected, missed, declined
    "incoming": false, // 是否为来电
    "aonly": true // 是否仅音频通话
  }
}
```
