# IM 国际化与多语言本地化指南

**重要说明**：进行多语言翻译时，请基于 `devel` 开发分支进行。

---

## 1. Web 客户端 (Webapp)

Web 端的翻译分布在两个位置：`src/i18n/` 和 `service-worker.js`。

添加新语言翻译时，将 `/src/i18n/en.json` 复制为 `/src/i18n/XX.json`（`XX` 为目标语言代码）。只需翻译 `"translation":` 这一行值，**不要** 翻译 `"defaultMessage"` 和 `"description"`：

```js
"action_block_contact": {
  "translation": "屏蔽联系人", // <<<---- 仅翻译此行字符串
  "defaultMessage": "Block Contact",  // 英文默认文本
  "description": "Flat button [Block Contact]", // 解释该文本的使用场景位置
  "missing": false,
  "obsolete": false
},
```

在 `service-worker.js` 中，直接添加对应的语言键值（如 `"new_message"` 与 `"new_chat"`）：

```js
const i18n = {
  ...
  'zh': {
    'new_message': "新消息",
    'new_chat': "新会话",
  },
  ...
}
```

---

## 3. Android 客户端

Android 端需翻译的文件为 `app/src/main/res/values/strings.xml`。

在 `app/src/main/res` 目录下创建新的 `values-XX` 目录（例如中文为 `values-zh` 或 `values-zh-rCN`）。将英文 `strings.xml` 复制到该目录下，翻译所有未标记 `translatable="false"` 的节点文本即可。

---

## 4. iOS 客户端

iOS 本地化可以通过翻译导出文件 `Localized Contents/en.xliff` 实现。只需翻译 `<target>` 节点之间的文本：

```xml
<trans-unit id="Action failed: %@" xml:space="preserve">
  <source>Action failed: %@</source> <!-- 英文默认文本 -->
  <target>操作失败: %@</target> <!-- 仅翻译此 target 节点内的字符串 -->
  <note>Toast notification</note>
</trans-unit>
```
