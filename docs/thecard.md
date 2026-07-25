# theCard：个人与主题描述格式规范

IM 使用 `theCard` 结构来存储和传输用户个人 Profile 或 Topic 主题的描述元数据。该格式在概念上类似于 [vCard 3.0](https://www.rfc-editor.org/rfc/rfc6350.txt) 规范。

在采用 JSON 表达 `theCard` 数据时，它与 [jCard](https://tools.ietf.org/html/rfc7095) 的表示方式不同（`theCard` 与 `jCard` 不兼容）。主要区别在于 `theCard` 使用 JSON 对象结构来表达逻辑关联的数据，而 `jCard` 使用有序数组。

`theCard` 的典型 JSON 对象结构如下：

```js
{
  fn: "Alice Johnson", // 字符串，用户或主题的格式化显示名称。
  photo: { // 对象，头像图片数据；'data' 或 'ref' 必须存在其一，其他字段为可选。
    type: "jpeg", // 字符串，图片 MIME 类型（省去了 'image/' 前缀）。
    data: "Rt53jUU...iVBORw0KGgoA==", // 字符串，Base64 编码的二进制图片数据。
    ref: "https://example.com/file/s/abcdef12345.jpg", // 字符串，图片的网络 URL 地址。
    width: 512, // 整数，图片像素宽度。
    height: 512, // 整数，图片像素高度。
    size: 123456 // 整数，图片字节大小。
  },
  note: "个人签名/主题描述", // 字符串，个人个性签名或主题简述。

  // 扩展规范（部分客户端可选支持）：

  n: { // 对象，用户的结构化姓名。
    surname: "Johnson", // 姓氏。
    given: "Alice", // 名字。
    additional: "", // 别名或中间名。
    prefix: "Ms.", // 前缀（如称谓或头衔）。
    suffix: "", // 后缀。
  },
  org: { // 对象，用户或主题归属的组织/公司信息。
    fn: "Acme Corp", // 字符串，组织或公司名称。
    title: "CEO", // 字符串，职位头衔。
  },
  comm: [ // 数组，定义联系与沟通方式。
    {
      des: ["home", "voice"], // 标记分类（可选）。
      proto: "tel", // 通信协议（必填）。
      value: "+17025551234" // 电话号码。
    },
    {
      des: ["work"],
      proto: "email",
      value: "alice@example.com", // 邮箱地址。
    },
    {
      des: ["other"],
      proto: "im",
      value: "im:topic/usrRkDVe0PYDOo", // IM 内部 URI 地址。
    },
    {
      proto: "http", // HTTP / HTTPS 个人主页或网站地址。
      value: "https://example.com", // 网站 URL。
    }
  ],
  bday: { // 对象，生日信息。
    y: 1995, // 整数，出生年份
    m: 8, // 整数，月份 1..12
    d: 18 // 整数，日期 1..31
  }
}
```

所有字段均为可选。目前 IM 标准客户端核心使用 `fn`（显示名）、`photo`（头像）、`note`（简介/签名）、`org`（组织）与 `comm`（联系方式）字段。
